package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestLiveWorkerCredentialRotationAndRevocation(t *testing.T) {
	grants := []AccessGrant{{Token: "old", WorkerID: "one", Permissions: []string{"worker"}}}
	var loadErr error
	called := 0
	h := WithReloadingAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(204)
	}), "admin", func() (string, []AccessGrant, error) { return "cluster", grants, loadErr })
	check := func(token, method, path, body string, status int) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		before := called
		h.ServeHTTP(resp, req)
		if resp.Code != status || (status != 204 && called != before) {
			t.Fatalf("%s %s: status=%d calls=%d", token, path, resp.Code, called-before)
		}
	}
	check("old", "POST", "/v1/workers/register", `{"workerId":"one"}`, 204)
	check("old", "POST", "/v1/workers/one/heartbeat", `{}`, 204)
	check("old", "GET", "/v1/workers/two/assignments", "", 403)
	// Overlap permits rollout without changing the worker or instance identity.
	grants = append(grants, AccessGrant{Token: "new", WorkerID: "one", Permissions: []string{"worker"}})
	check("new", "GET", "/v1/workers/one/assignments", "", 204)
	grants[0].Revoked = true
	check("old", "GET", "/v1/workers/one/assignments", "", 403)
	check("old", "POST", "/v1/workers/one/heartbeat", `{}`, 403)
	check("old", "POST", "/v1/workers/register", `{"workerId":"one"}`, 403)
	check("new", "GET", "/v1/workers/one/assignments", "", 204)
	grants[1].ExpiresAt = "2000-01-01T00:00:00Z"
	check("new", "GET", "/v1/workers/one/assignments", "", 403)
	loadErr = errors.New("unreadable configuration")
	check("new", "GET", "/v1/workers/one/assignments", "", 503)
}

func TestWorkerProtocolNegotiation(t *testing.T) {
	for _, tc := range []struct {
		versions string
		status   int
	}{
		{`["v2","v1"]`, 200}, {`["v2"]`, 400}, {`[]`, 200},
	} {
		h := NewWorkerProtocolHandler(&workerBackendFixture{})
		r := httptest.NewRequest("POST", "/v1/workers/register", strings.NewReader(`{"workerId":"one","capacity":1,"protocolVersion":"v1","supportedProtocolVersions":`+tc.versions+`}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.versions, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			var result WorkerRegistrationResult
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ProtocolVersion != "v1" {
				t.Fatalf("selection: %+v %v", result, err)
			}
		}
	}
}

func TestArtifactProxyRanges(t *testing.T) {
	driver, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := driver.PutStream(t.Context(), strings.NewReader("abcdef"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouterWithArtifacts(state.New(), nil, driver)
	for _, tc := range []struct {
		value  string
		status int
		body   string
	}{
		{"bytes=2-", 206, "cdef"}, {"bytes=0-", 206, "abcdef"}, {"bytes=6-", 416, ""}, {"bytes=-1", 416, ""}, {"bytes=1-2", 416, ""},
	} {
		r := httptest.NewRequest("GET", "/v1/artifacts/"+ref.Digest, nil)
		r.Header.Set("Range", tc.value)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Body.String() != tc.body {
			t.Fatalf("range %q: %d %q", tc.value, w.Code, w.Body.String())
		}
		if tc.status == 206 && w.Header().Get("Content-Range") == "" {
			t.Fatal("missing content range")
		}
	}
}
