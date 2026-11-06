package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func digestOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func artifactTestServer(t *testing.T, local *Local, planner Planner) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remainder := strings.TrimPrefix(r.URL.Path, "/v1/artifacts/")
		if strings.HasSuffix(remainder, "/transfer") {
			digest := strings.TrimSuffix(remainder, "/transfer")
			method := r.URL.Query().Get("method")
			if method == "" {
				method = http.MethodGet
			}
			size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
			base := &url.URL{Scheme: "http", Host: r.Host}
			plan, err := planner.Plan(r.Context(), digest, method, size, base)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(plan)
			return
		}
		switch r.Method {
		case http.MethodPut:
			ref, err := local.PutStream(r.Context(), r.Body, r.Header.Get("Content-Type"), remainder)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, ErrIntegrity) {
					status = http.StatusUnprocessableEntity
				}
				http.Error(w, err.Error(), status)
				return
			}
			_ = json.NewEncoder(w).Encode(ref)
		case http.MethodGet:
			stream, ref, err := local.GetStream(r.Context(), remainder)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			defer stream.Close()
			w.Header().Set("X-Artifact-SHA256", ref.Digest)
			_, _ = io.Copy(w, stream)
		}
	}))
}

func TestTransferClientDigestMismatch(t *testing.T) {
	payload := "expected-bytes"
	wrongDigest := digestOf("other-bytes")
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := artifactTestServer(t, local, Planner{Driver: local, Threshold: 1 << 20})
	defer srv.Close()

	client := Client{BaseURL: srv.URL}
	_, err = client.Upload(context.Background(), wrongDigest, "text/plain", int64(len(payload)), strings.NewReader(payload))
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected ErrIntegrity, got %v", err)
	}
}

func TestTransferSmallProxyRoundTrip(t *testing.T) {
	payload := "small-proxy-payload"
	digest := digestOf(payload)
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	planner := Planner{Driver: local, Threshold: 1024}
	srv := artifactTestServer(t, local, planner)
	defer srv.Close()

	client := Client{BaseURL: srv.URL}
	ref, err := client.Upload(context.Background(), digest, "text/plain", int64(len(payload)), strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !local.Has(digest) {
		t.Fatal("artifact not stored locally")
	}
	var buf strings.Builder
	if err := client.Download(context.Background(), digest, ref.Size, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != payload {
		t.Fatalf("got %q", buf.String())
	}
}

func TestTransferLargeObjectDirectPath(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	mux := http.NewServeMux()
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s3Client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/v1/artifacts/") {
			return http.DefaultTransport.RoundTrip(r)
		}
		if !strings.Contains(r.URL.RawQuery, "X-Amz-Algorithm") {
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
		}
		q := r.URL.Query()
		for _, header := range strings.Split(q.Get("X-Amz-SignedHeaders"), ";") {
			if header != "host" && r.Header.Get(header) == "" {
				t.Errorf("missing signed header %s", header)
			}
		}
		expectedHash := "UNSIGNED-PAYLOAD"
		if r.Method == http.MethodPut {
			expectedHash = strings.TrimPrefix(r.URL.Path, "/bucket/")
		}
		if r.Header.Get("X-Amz-Content-Sha256") != expectedHash || r.Header.Get("X-Amz-Date") != q.Get("X-Amz-Date") || r.Header.Get("X-Amz-Security-Token") != "session-token" {
			t.Error("signed header values do not match signature inputs")
		}
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			data, _ := io.ReadAll(r.Body)
			objects[key] = data
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
		case http.MethodGet:
			data, ok := objects[key]
			if !ok {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(data)), Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header), Request: r}, nil
		}
		return nil, errors.New("unexpected presigned request")
	})}
	s3, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "bucket", AccessKey: "test-key", SecretKey: "test-secret", SessionToken: "session-token", Client: s3Client})
	if err != nil {
		t.Fatal(err)
	}
	planner := Planner{Driver: Tiered{S3: s3, Local: local, Threshold: 8}, Threshold: 8, S3: s3}
	mux.HandleFunc("/v1/artifacts/", func(w http.ResponseWriter, r *http.Request) {
		remainder := strings.TrimPrefix(r.URL.Path, "/v1/artifacts/")
		if strings.HasSuffix(remainder, "/transfer") {
			digest := strings.TrimSuffix(remainder, "/transfer")
			size, _ := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
			plan, err := planner.Plan(r.Context(), digest, r.URL.Query().Get("method"), size, &url.URL{Scheme: "http", Host: r.Host})
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if plan.Mode != "direct" {
				t.Fatalf("expected direct transfer, got %+v", plan)
			}
			_ = json.NewEncoder(w).Encode(plan)
			return
		}
	})

	payload := strings.Repeat("L", 16)
	digest := digestOf(payload)
	client := Client{BaseURL: srv.URL, HTTP: s3Client}
	ref, err := client.Upload(context.Background(), digest, "application/octet-stream", int64(len(payload)), strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	stored := objects[digest[7:]]
	mu.Unlock()
	if string(stored) != payload || ref.Size != int64(len(payload)) {
		t.Fatalf("stored=%q ref=%+v", stored, ref)
	}
	var downloaded strings.Builder
	if err := client.Download(t.Context(), digest, ref.Size, &downloaded); err != nil || downloaded.String() != payload {
		t.Fatalf("direct download: %v %q", err, downloaded.String())
	}

}

func TestPlannerFromTieredDriver(t *testing.T) {
	local, _ := NewLocal(t.TempDir())
	s3, _ := NewS3(S3Config{Endpoint: "http://objects.test", Bucket: "bucket", AccessKey: "k", SecretKey: "s", Client: http.DefaultClient})
	driver := Tiered{S3: s3, Local: local, Threshold: 99}
	p := PlannerFromDriver(driver)
	if p.Threshold != 99 || p.S3 == nil {
		t.Fatalf("planner=%+v", p)
	}
}
