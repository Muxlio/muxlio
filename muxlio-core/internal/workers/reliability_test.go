package workers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteWorkerReregistersAndRetriesCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registrations, polls, completions := 0, 0, 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "register"):
			registrations++
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(r.URL.Path, "heartbeat"):
			if registrations < 2 {
				w.WriteHeader(409)
				return
			}
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(r.URL.Path, "complete"):
			completions++
			if completions == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(202)
			cancel()
		case strings.HasSuffix(r.URL.Path, "assignments"):
			polls++
			if polls > 1 {
				w.WriteHeader(204)
				return
			}
			_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "a", RunID: "r", ActivationID: "n", AttemptID: "a", CoordinatorEpoch: 1, Generation: 1, Request: json.RawMessage(`{"executor":"test"}`)})
		}
	})
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		response := recorder.Result()
		response.Body = io.NopCloser(strings.NewReader(recorder.Body.String()))
		return response, nil
	})}
	fake := NewFake()
	err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", HTTPClient: client, WorkerID: "w", PollInterval: time.Millisecond}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if registrations != 2 || completions != 2 || len(fake.Calls()) != 1 {
		t.Fatalf("registrations=%d completions=%d executions=%d", registrations, completions, len(fake.Calls()))
	}
}

func testScript(t *testing.T, body string) *ScriptExecutor {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test"), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test.json"), []byte(`{"manifestVersion":"1","executor":"test","capabilities":{"artifacts":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return NewScriptExecutor(dir, t.TempDir())
}

func TestAdapterTimeoutKillsDescendants(t *testing.T) {
	s := testScript(t, "sleep 30 &\nwait\n")
	s.Timeout = 50 * time.Millisecond
	start := time.Now()
	result, err := s.Execute(ExecuteRequest{Executor: "test"})
	if err != nil || !strings.Contains(result.Error, "timed out") {
		t.Fatalf("%+v %v", result, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout failed to reap process tree")
	}
}

func TestAdapterFailureAndOversizedOutput(t *testing.T) {
	s := testScript(t, `printf '{"outputs":{"passed":true}}'; exit 7`)
	r, err := s.Execute(ExecuteRequest{Executor: "test"})
	if err != nil || !strings.Contains(r.Error, "exit status 7") {
		t.Fatalf("nonzero exit accepted: %+v %v", r, err)
	}
	s = testScript(t, `dd if=/dev/zero of=large bs=1048576 count=5 2>/dev/null; printf '{}'`)
	r, err = s.Execute(ExecuteRequest{Executor: "test", Outputs: map[string]OutputDecl{"large": {Path: "large"}}})
	if err != nil || !strings.Contains(r.Error, "exceeds") {
		t.Fatalf("oversized output accepted: %+v %v", r, err)
	}
}

func TestAdapterEnvironmentAndLogBounds(t *testing.T) {
	t.Setenv("MUXLIO_PRIVATE_TEST_SECRET", "must-not-inherit")
	s := testScript(t, `if [ -n "$MUXLIO_PRIVATE_TEST_SECRET" ]; then exit 1; fi
 printf '{"outputs":{"clean":true}}'
 i=0; while [ "$i" -lt 10000 ]; do printf 'secret-value-0123456789' >&2; i=$((i+1)); done
 `)
	r, err := s.Execute(ExecuteRequest{Executor: "test", Secrets: map[string]string{"token": "secret-value"}, Policy: &ExecutionPolicy{AllowedSecrets: []string{"token"}}})
	if err != nil || r.Error != "" || r.Outputs["clean"] != true {
		t.Fatalf("%+v %v", r, err)
	}
	if len(r.Logs) > MaxLogBytes+20 || strings.Contains(r.Logs, "secret-value") {
		t.Fatalf("logs not bounded/redacted: %d", len(r.Logs))
	}
}

func TestAdapterExplicitAgentHome(t *testing.T) {
	t.Setenv("MUXLIO_PRIVATE_TEST_SECRET", "must-not-inherit")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "provider-login"), []byte("dedicated-auth"), 0600); err != nil {
		t.Fatal(err)
	}
	s := testScript(t, `if [ -n "$MUXLIO_PRIVATE_TEST_SECRET" ]; then exit 2; fi
 if [ "$HOME" = "$TMPDIR" ]; then exit 3; fi
 if [ "$(cat "$HOME/provider-login")" != dedicated-auth ]; then exit 4; fi
 printf '{"outputs":{"authenticated":true}}'
 `)
	s.AgentHome = home
	env, envErr := s.environment("/scratch", true, "test")
	if envErr != nil {
		t.Fatal(envErr)
	}
	if !strings.Contains(strings.Join(env, "\n"), "HOME=/scratch\n") || strings.Contains(strings.Join(env, "\n"), "HOME="+home) {
		t.Fatalf("isolated adapter received agent home: %v", env)
	}
	r, err := s.Execute(ExecuteRequest{Executor: "test"})
	if err != nil || r.Error != "" || r.Outputs["authenticated"] != true {
		t.Fatalf("dedicated home unavailable: %+v %v", r, err)
	}
	for _, invalid := range []string{"relative", filepath.Join(home, "missing"), filepath.Join(home, "provider-login")} {
		s.AgentHome = invalid
		if _, err := s.Execute(ExecuteRequest{Executor: "test"}); err == nil {
			t.Fatalf("invalid agent home accepted: %q", invalid)
		}
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAdapterReconciliationReasonRedactedBeforePersistence(t *testing.T) {
	s := testScript(t, `printf '{"reconciliation":{"status":"completed","reason":"ledger checked with secret-value"}}'`)
	r, err := s.Execute(ExecuteRequest{Executor: "test", Secrets: map[string]string{"token": "secret-value"}, Policy: &ExecutionPolicy{AllowedSecrets: []string{"token"}}})
	if err != nil || r.Error != "" || r.Reconciliation == nil || r.Reconciliation.Status != "completed" {
		t.Fatalf("reconciliation lost: %+v %v", r, err)
	}
	if strings.Contains(r.Reconciliation.Reason, "secret-value") || !strings.Contains(r.Reconciliation.Reason, "ledger checked") {
		t.Fatalf("unsafe reconciliation reason: %q", r.Reconciliation.Reason)
	}
}
