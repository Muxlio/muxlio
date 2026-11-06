package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestScopedRunCredentials(t *testing.T) {
	for _, tc := range []struct {
		method, path, permission string
		allowed                  bool
	}{
		{"POST", "/v1/runs/own/cancel", "control", true},
		{"POST", "/v1/runs/other/cancel", "control", false},
		{"POST", "/v1/runs/own/signals/answer", "signals", true},
		{"POST", "/v1/runs/other/signals/answer", "signals", false},
		{"POST", "/v1/runs/own/signals/answer", "control", false},
		{"POST", "/v1/runs/own/cancel", "signals", false},
		{"POST", "/v1/runs/own/steer", "control", false},
		{"POST", "/v1/runs/own/steer", "steer", true},
		{"GET", "/v1/runs/own", "read", true},
		{"GET", "/v1/runs", "read", false},
		{"POST", "/v1/workers/register", "control", false},
	} {
		t.Run(tc.permission+tc.path, func(t *testing.T) {
			called := false
			h := WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }), "admin", "cluster-worker", AccessGrant{Token: "scoped", RunID: "own", Permissions: []string{tc.permission}})
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, authRequest(tc.method, tc.path, "scoped", "203.0.113.1:9"))
			if called != tc.allowed || (!tc.allowed && resp.Code != 403) {
				t.Fatalf("allowed=%v status=%d", called, resp.Code)
			}
		})
	}
}

func TestScopedWorkerAndArtifactCredentials(t *testing.T) {
	grant := AccessGrant{Token: "worker-one", WorkerID: "one", Permissions: []string{"worker", "artifacts:read"}, ArtifactDigests: []string{"sha256:allowed"}}
	for _, tc := range []struct {
		method, path, body string
		allowed            bool
	}{
		{"POST", "/v1/workers/register", `{"workerId":"one"}`, true},
		{"POST", "/v1/workers/register", `{"workerId":"two"}`, false},
		{"GET", "/v1/workers/one/assignments", "", true},
		{"GET", "/v1/workers/two/assignments", "", false},
		{"POST", "/v1/runs/own/cancel", "", false},
		{"GET", "/v1/artifacts/sha256:allowed/transfer?method=GET", "", true},
		{"GET", "/v1/artifacts/sha256:other/transfer?method=GET", "", false},
		{"GET", "/v1/artifacts/sha256:allowed/transfer?method=PUT", "", false},
		{"PUT", "/v1/artifacts/sha256:allowed", "", false},
	} {
		called := false
		h := WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }), "admin", "cluster", grant)
		req := authRequest(tc.method, tc.path, grant.Token, "203.0.113.1:9")
		if tc.body != "" {
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+grant.Token)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if called != tc.allowed {
			t.Fatalf("%s %s allowed=%v", tc.method, tc.path, called)
		}
	}
}

func TestUnauthenticatedRemoteRequestsFailClosed(t *testing.T) {
	h := WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unauthenticated remote request reached handler")
	}), "", "")
	for _, path := range []string{"/v1/runs/other/cancel", "/v1/runs/other/signals/answer", "/v1/workers/register"} {
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, authRequest("POST", path, "", "203.0.113.1:9"))
		if resp.Code != 401 {
			t.Fatalf("%s status=%d", path, resp.Code)
		}
	}
}

func TestCoordinatorRejectsCredentialValuesBeforeDispatch(t *testing.T) {
	c := NewCoordinator(state.New())
	_, err := c.Execute(workers.ExecuteRequest{Executor: "test", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Secrets: map[string]string{"api": "raw-credential"}, Policy: &workers.ExecutionPolicy{AllowedSecrets: []string{"api"}}})
	if err == nil || !strings.Contains(err.Error(), "secret handles") || strings.Contains(err.Error(), "raw-credential") {
		t.Fatalf("unsafe credential dispatch: %v", err)
	}
}
