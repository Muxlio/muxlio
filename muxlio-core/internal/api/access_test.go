package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
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
		{"POST", "/v1/runs/own/attempts/a/resolve", "control", true},
		{"POST", "/v1/runs/other/attempts/a/resolve", "control", false},
		{"POST", "/v1/runs/own/attempts/a/resolve", "read", false},
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

// The registration route name is also a valid worker identity.
func TestScopedWorkerNamedRegister(t *testing.T) {
	for _, endpoint := range []struct{ method, suffix string }{
		{"POST", "heartbeat"}, {"GET", "assignments"},
		{"POST", "assignments/log"}, {"POST", "assignments/complete"},
	} {
		for _, worker := range []string{"one", "register"} {
			t.Run(worker+"/"+endpoint.suffix, func(t *testing.T) {
				called := false
				h := WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }), "admin", "cluster",
					AccessGrant{Token: "scoped", WorkerID: worker, Permissions: []string{"worker"}})
				body := ""
				if worker == "one" {
					body = `{"workerId":"one"}`
				}
				req := httptest.NewRequest(endpoint.method, "/v1/workers/register/"+endpoint.suffix, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer scoped")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if worker == "one" && (called || w.Code != 403) {
					t.Fatalf("cross-worker request reached backend=%v, status=%d", called, w.Code)
				}
				if worker == "register" && !called {
					t.Fatalf("own protocol request denied: %d", w.Code)
				}
			})
		}
	}
}

func TestScopedRegisterWorkerCannotClaimOrComplete(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "workers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "private"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(s)
	h := WithAuth(NewRouter(s, nil, NewWorkerProtocolHandler(c)), "admin", "cluster",
		AccessGrant{Token: "attacker", WorkerID: "one", Permissions: []string{"worker"}},
		AccessGrant{Token: "victim", WorkerID: "register", Permissions: []string{"worker"}})
	call := func(method, path, token, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	call("POST", "/v1/workers/register", "victim", `{"workerId":"register","capacity":1,"protocolVersion":"v1","capabilities":[{"name":"test"}]}`, 200)
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "attempt", RunID: "private", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{"executor":"test","params":{"task":"private-task"}}`)}); err != nil {
		t.Fatal(err)
	}
	call("POST", "/v1/workers/register/heartbeat", "attacker", `{"workerId":"one","protocolVersion":"v1","capacity":0}`, 403)
	call("GET", "/v1/workers/register/assignments", "attacker", `{"workerId":"one"}`, 403)
	for _, endpoint := range []string{"log", "complete"} {
		call("POST", "/v1/workers/register/assignments/"+endpoint, "attacker", `{"workerId":"one"}`, 403)
	}
	assignments, err := s.ListAssignments("private")
	if err != nil || len(assignments) != 0 {
		t.Fatalf("unauthorized claim: %+v %v", assignments, err)
	}
	workers, err := s.ListWorkers(time.Now().Add(-time.Minute))
	if err != nil || len(workers) != 1 || workers[0].Capacity != 1 {
		t.Fatalf("capacity mutated: %+v %v", workers, err)
	}
	call("POST", "/v1/workers/register/heartbeat", "victim", `{"protocolVersion":"v1"}`, 200)
	w := call("GET", "/v1/workers/register/assignments", "victim", "", 200)
	var a WorkerAssignment
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.AssignmentID != "attempt" {
		t.Fatalf("victim lost assignment: %+v", a)
	}
	// Even with the complete assignment identity the other worker cannot write.
	body, _ := json.Marshal(map[string]any{"workerId": "one", "assignmentId": a.AssignmentID, "runId": a.RunID, "activationId": a.ActivationID, "attemptId": a.AttemptID, "coordinatorEpoch": a.CoordinatorEpoch, "generation": a.Generation, "state": "succeeded", "outputs": map[string]any{"forged": true}})
	call("POST", "/v1/workers/register/assignments/complete", "attacker", string(body), 403)
	result, err := s.WorkerResult("attempt")
	if err != nil || len(result) != 0 {
		t.Fatalf("forged result persisted: %s %v", result, err)
	}
	logs, err := s.GetLogChunks("private", "activation")
	if err != nil || len(logs) != 0 {
		t.Fatalf("unauthorized logs: %+v %v", logs, err)
	}
	call("POST", "/v1/workers/register/assignments/complete", "victim", string(body), 202)
}
