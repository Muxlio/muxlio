package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTransferRedirectContainment(t *testing.T) {
	for _, stage := range []string{"plan", "proxy", "direct", "s3"} {
		t.Run(stage, func(t *testing.T) {
			reached := false
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				t.Errorf("redirect reached another origin: auth=%q session=%q", r.Header.Get("Authorization"), r.Header.Get("X-Amz-Security-Token"))
				io.WriteString(w, "payload")
			}))
			defer sink.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stage != "plan" && strings.HasSuffix(r.URL.Path, "/transfer") {
					mode, target := "proxy", "/blob"
					if stage == "direct" {
						mode, target = "direct", "http://"+r.Host+"/blob"
					}
					json.NewEncoder(w).Encode(TransferPlan{Digest: digestOf("payload"), Method: "GET", Mode: mode, URL: target, Headers: map[string]string{"X-Amz-Security-Token": "fake-session"}})
					return
				}
				http.Redirect(w, r, sink.URL, http.StatusFound)
			}))
			defer origin.Close()
			if stage == "s3" {
				s, _ := NewS3(S3Config{Endpoint: origin.URL, Bucket: "bucket", AccessKey: "fake-key", SecretKey: "fake-secret", SessionToken: "fake-session"})
				r, _, err := s.GetStream(t.Context(), digestOf("payload"))
				if r != nil {
					r.Close()
				}
				if err == nil {
					t.Error("redirect accepted")
				}
			} else {
				err := (Client{BaseURL: origin.URL, Token: "fake-bearer"}).Download(t.Context(), digestOf("payload"), 7, &strings.Builder{})
				if err == nil {
					t.Error("redirect accepted")
				}
			}
			if reached {
				t.Error("cross-origin destination contacted")
			}
		})
	}
}

func TestUploadRejectsRedirects(t *testing.T) {
	for _, mode := range []string{"proxy", "direct"} {
		for _, code := range []int{301, 302, 303, 307, 308} {
			t.Run(mode+"/"+strconv.Itoa(code), func(t *testing.T) {
				landed := false
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/transfer") {
						target := "/blob"
						if mode == "direct" {
							target = "http://" + r.Host + target
						}
						json.NewEncoder(w).Encode(TransferPlan{Digest: digestOf("payload"), Method: "PUT", Mode: mode, URL: target})
					} else if r.URL.Path == "/blob" {
						http.Redirect(w, r, "/landing", code)
					} else {
						landed = true
						io.WriteString(w, "login page")
					}
				}))
				defer srv.Close()
				ref, err := (Client{BaseURL: srv.URL}).Upload(t.Context(), digestOf("payload"), "", 7, strings.NewReader("payload"))
				if err == nil || ref.Digest != "" || landed {
					t.Fatalf("ref=%+v err=%v landed=%v", ref, err, landed)
				}
			})
		}
	}
}

func TestS3RealTransportUpload(t *testing.T) {
	var stored string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { data, _ := io.ReadAll(r.Body); stored = string(data) }))
	defer srv.Close()
	s, _ := NewS3(S3Config{Endpoint: srv.URL, Bucket: "bucket", AccessKey: "fake", SecretKey: "fake"})
	local, _ := NewLocal(t.TempDir())
	driver := Tiered{S3: s, Local: local, Threshold: 1}
	ref, err := s.PutStream(t.Context(), strings.NewReader("payload"), "", "")
	if err != nil || ref.Digest != digestOf("payload") || stored != "payload" {
		t.Fatalf("ref=%+v err=%v stored=%q", ref, err, stored)
	}
	ref, err = driver.PutStream(t.Context(), strings.NewReader("payload"), "", "")
	if err != nil || local.Has(ref.Digest) {
		t.Fatalf("unnecessary local fallback: %+v %v", ref, err)
	}
}

func TestRecoveredArtifactDownloadPlan(t *testing.T) {
	for _, kind := range []string{"tiered", "fallback"} {
		for _, recovered := range []bool{false, true} {
			t.Run(kind+"/"+strconv.FormatBool(recovered), func(t *testing.T) {
				payload := strings.Repeat("a", (1<<20)+1)
				var available atomic.Bool
				available.Store(!recovered)
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "PUT" {
						io.Copy(io.Discard, r.Body)
						if !available.Load() {
							w.WriteHeader(503)
						}
					} else if available.Load() {
						io.WriteString(w, payload)
					} else {
						w.WriteHeader(404)
					}
				}))
				defer remote.Close()
				s, _ := NewS3(S3Config{Endpoint: remote.URL, Bucket: "bucket", AccessKey: "fake", SecretKey: "fake"})
				local, _ := NewLocal(t.TempDir())
				var driver Driver = Tiered{S3: s, Local: local, Threshold: 1}
				if kind == "fallback" {
					driver = Fallback{Primary: s, Local: local}
				}
				ref, err := driver.PutStream(t.Context(), strings.NewReader(payload), "", "")
				if err != nil {
					t.Fatal(err)
				}
				if !recovered {
					// Retain a verified local copy, then lose the remote object.
					if _, err := local.PutStream(t.Context(), strings.NewReader(payload), "", ref.Digest); err != nil {
						t.Fatal(err)
					}
					available.Store(false)
				}
				srv := artifactTestServer(t, driver, PlannerFromDriver(driver))
				defer srv.Close()
				var dest strings.Builder
				if err := (Client{BaseURL: srv.URL}).Download(t.Context(), ref.Digest, ref.Size, &dest); err != nil || dest.String() != payload {
					t.Fatalf("download: %v bytes=%d", err, dest.Len())
				}
			})
		}
	}
}

func TestLocalPutVerifiesExistingAndRestores(t *testing.T) {
	local, _ := NewLocal(t.TempDir())
	blob, err := local.Put([]byte("payload"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := local.path(blob.Digest)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := local.Put([]byte("payload"), ""); !errors.Is(err, ErrIntegrity) || got != nil {
		t.Fatalf("corrupt dedup: %+v %v", got, err)
	}
	if err := local.Expire(blob.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Put([]byte("payload"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if local.expired(blob.Digest) || !local.Has(blob.Digest) {
		t.Fatal("expiration not restored")
	}
}

// Use a synthetic transport to cover HTTPS and subdomains without external DNS
// or credentials. The same policy wraps client plans, transfers and S3 calls.
func TestArtifactRedirectOriginsAndCallerPolicy(t *testing.T) {
	for _, target := range []string{
		"https://objects.test:444/blob", "https://other.test/blob",
		"https://sub.objects.test/blob", "http://objects.test/blob",
		"https://objects.test:443/next",
	} {
		t.Run(target, func(t *testing.T) {
			for _, method := range []string{"GET", "PUT"} {
				calls, policies := 0, 0
				original := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					header := make(http.Header)
					status := 200
					if calls == 1 {
						status = 302
						header.Set("Location", target)
					}
					return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}), CheckRedirect: func(r *http.Request, via []*http.Request) error { policies++; return nil }}
				req, _ := http.NewRequest(method, "https://objects.test/blob", strings.NewReader("payload"))
				resp, err := containedHTTPClient(original).Do(req)
				if resp != nil {
					resp.Body.Close()
				}
				allowed := method == "GET" && target == "https://objects.test:443/next"
				if allowed {
					if err != nil || calls != 2 || policies != 1 {
						t.Fatalf("allowed redirect: %v calls=%d policies=%d", err, calls, policies)
					}
				} else if err == nil || calls != 1 || policies != 0 {
					t.Fatalf("unsafe redirect: %v calls=%d policies=%d", err, calls, policies)
				}
				// The wrapper must not mutate the supplied client's redirect policy.
				u, _ := url.Parse(target)
				if err := original.CheckRedirect(&http.Request{URL: u}, nil); err != nil || policies == 0 {
					t.Fatal("original policy changed")
				}
			}
		})
	}
	denied := errors.New("caller denies redirect")
	client := containedHTTPClient(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return denied }})
	req, _ := http.NewRequest("GET", "https://objects.test/blob", nil)
	if err := client.CheckRedirect(req, []*http.Request{req}); !errors.Is(err, denied) {
		t.Fatalf("caller rejection lost: %v", err)
	}
	client = containedHTTPClient(&http.Client{})
	if err := client.CheckRedirect(req, make([]*http.Request, 0)); err == nil {
		t.Fatal("missing initial request accepted")
	}
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = req
	}
	if err := client.CheckRedirect(req, via); err == nil {
		t.Fatal("default redirect limit lost")
	}
}

func TestS3UploadFailureCleanup(t *testing.T) {
	for _, failure := range []string{"status", "transport", "cancellation", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			staging := t.TempDir()
			t.Setenv("TMPDIR", staging)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if failure == "redirect" {
					http.Redirect(w, r, "/landing", 302)
				} else {
					w.WriteHeader(503)
				}
			}))
			defer srv.Close()
			client := srv.Client()
			if failure == "transport" || failure == "cancellation" {
				client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					defer r.Body.Close()
					if failure == "cancellation" {
						cancel()
						return nil, ctx.Err()
					}
					return nil, errors.New("synthetic transport failure")
				})}
			}
			s, _ := NewS3(S3Config{Endpoint: srv.URL, Bucket: "bucket", AccessKey: "fake", SecretKey: "fake", Client: client})
			ref, err := s.PutStream(ctx, strings.NewReader("payload"), "", "")
			if err == nil || ref.Digest != "" {
				t.Fatalf("false upload success: %+v %v", ref, err)
			}
			files, err := os.ReadDir(staging)
			if err != nil || len(files) != 0 {
				t.Fatalf("staging leak: %v %v", files, err)
			}
		})
	}
}
