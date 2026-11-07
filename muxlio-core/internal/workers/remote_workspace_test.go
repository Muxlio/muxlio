package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrepareRemoteDispatchOmitsWorkspace(t *testing.T) {
	req := ExecuteRequest{
		Workspace: "/coordinator/project",
		Executor:  "agent/test",
		RunID:     "run",
		Params:    json.RawMessage(`{"k":"v"}`),
	}
	got := PrepareRemoteDispatch(req)
	if got.Workspace != "" {
		t.Fatalf("workspace should be omitted: %q", got.Workspace)
	}
	if got.Executor != req.Executor || got.RunID != req.RunID {
		t.Fatalf("unexpected dispatch payload: %#v", got)
	}
}

func TestRemotePollDoesNotBindWorkerOrInputWorkspace(t *testing.T) {
	for _, inputs := range []map[string]any{nil, {"workspace": "/missing/remote/worktree"}} {
		got, err := ApplyWorkerWorkspace(ExecuteRequest{Action: "poll", Workspace: "/original/worktree", Inputs: inputs}, t.TempDir())
		if err != nil || got.Workspace != "" {
			t.Fatalf("poll rebound workspace: %+v %v", got, err)
		}
	}
}

func TestRemoteReconcileDoesNotBindWorkspace(t *testing.T) {
	got, err := ApplyWorkerWorkspace(ExecuteRequest{Action: "reconcile", Inputs: map[string]any{"workspace": filepath.Join(t.TempDir(), "removed-worktree")}}, "")
	if err != nil || got.Workspace != "" {
		t.Fatalf("reconciliation rebound workspace: %+v %v", got, err)
	}
}

func TestApplyWorkerWorkspaceIgnoresMissingCoordinatorPath(t *testing.T) {
	coordinatorPath := filepath.Join(t.TempDir(), "only-on-coordinator")
	workerPath := t.TempDir()
	got, err := ApplyWorkerWorkspace(ExecuteRequest{Workspace: coordinatorPath}, workerPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != workerPath {
		t.Fatalf("worker workspace = %q, want %q", got.Workspace, workerPath)
	}
}

func TestApplyWorkerWorkspacePrefersExecuteInputPath(t *testing.T) {
	worktree := t.TempDir()
	workerDefault := t.TempDir()
	got, err := ApplyWorkerWorkspace(ExecuteRequest{
		Workspace: filepath.Join(t.TempDir(), "missing-coordinator-path"),
		Inputs:    map[string]any{"workspace": worktree},
	}, workerDefault)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != worktree {
		t.Fatalf("workspace = %q, want input worktree %q", got.Workspace, worktree)
	}
}

func TestApplyWorkerWorkspaceRejectsMissingInputWorkspace(t *testing.T) {
	workerPath := t.TempDir()
	_, err := ApplyWorkerWorkspace(ExecuteRequest{
		Inputs: map[string]any{"workspace": filepath.Join(t.TempDir(), "missing-worktree")},
	}, workerPath)
	if err == nil {
		t.Fatal("expected error when input workspace is missing on worker")
	}
}

func TestRemoteExecuteDoesNotRequireCoordinatorWorkspaceOnWorker(t *testing.T) {
	workerWorkspace := t.TempDir()
	coordinatorWorkspace := filepath.Join(t.TempDir(), "coordinator-workspace-missing-on-worker")
	requestBody, err := json.Marshal(ExecuteRequest{
		Workspace: coordinatorWorkspace,
		Executor:  "test",
		RunID:     "run",
	})
	if err != nil {
		t.Fatal(err)
	}

	fake := NewFake().ByExecutor("test", ExecuteResult{Outputs: map[string]any{"ok": true}})
	var completed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workers/register":
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workers/w/heartbeat":
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workers/w/assignments":
			if completed.Load() {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(remoteAssignment{
				AssignmentID: "a", RunID: "r", ActivationID: "n", AttemptID: "a",
				CoordinatorEpoch: 1, Generation: 1, Request: requestBody,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workers/w/assignments/complete":
			completed.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	err = RunRemoteWorker(ctx, RemoteWorkerConfig{
		CoordinatorURL: srv.URL,
		HTTPClient:     srv.Client(),
		WorkerID:       "w",
		PollInterval:   time.Millisecond,
		Workspace:      workerWorkspace,
	}, fake)
	if err != nil && err != context.Canceled {
		t.Fatalf("RunRemoteWorker: %v", err)
	}
	if !completed.Load() {
		t.Fatal("assignment did not complete")
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0].Workspace != workerWorkspace {
		t.Fatalf("executor workspace = %q, want worker workspace %q", calls[0].Workspace, workerWorkspace)
	}
	if calls[0].Workspace == coordinatorWorkspace {
		t.Fatalf("coordinator workspace leaked to executor")
	}
}
