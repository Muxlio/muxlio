package artifacts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestS3RoundTripAndIntegrity(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test-key/") {
			t.Errorf("missing SigV4 authorization")
		}
		if strings.Contains(r.Header.Get("Authorization"), "test-secret") {
			t.Errorf("secret key leaked into authorization")
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
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header), Request: r}, nil
		}
		return nil, errors.New("unexpected method")
	})}
	driver, err := NewS3(S3Config{Endpoint: "http://objects.test", Bucket: "bucket", AccessKey: "test-key", SecretKey: "test-secret", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := driver.PutStream(context.Background(), strings.NewReader("s3 payload"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	r, got, err := driver.GetStream(context.Background(), ref.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, _ := io.ReadAll(r)
	if string(data) != "s3 payload" || got.Size != int64(len(data)) {
		t.Fatalf("unexpected roundtrip %q %+v", data, got)
	}
	mu.Lock()
	objects[ref.Digest[7:]] = []byte("corrupt")
	mu.Unlock()
	if _, _, err := driver.GetStream(context.Background(), ref.Digest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("expected integrity error, got %v", err)
	}
}

func TestTieredUsesLocalForSmallAndFallsBackForLarge(t *testing.T) {
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewS3(S3Config{Endpoint: "http://127.0.0.1:1", Bucket: "bucket", AccessKey: "key", SecretKey: "secret", Client: &http.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	driver := Tiered{S3: remote, Local: local, Threshold: 3}
	small, err := driver.PutStream(context.Background(), strings.NewReader("ok"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !local.Has(small.Digest) {
		t.Fatal("small object was not stored locally")
	}
	large, err := driver.PutStream(context.Background(), strings.NewReader("large"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !local.Has(large.Digest) {
		t.Fatal("failed S3 upload did not fall back to local")
	}
	r, _, err := driver.GetStream(context.Background(), large.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, _ := io.ReadAll(r)
	if string(data) != "large" {
		t.Fatalf("got %q", data)
	}
}
