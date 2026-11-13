package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestWorkspaceSubmissionPreflight(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	c := &Coordinator{Workspaces: workers.WorkspaceRegistry{
		"project": {WorkerID: "local", Path: root, Aliases: []string{"client-alias"}},
		"remote":  {WorkerID: "remote", Path: "/missing/worktree"},
	}}
	for _, tc := range []struct {
		name, id, mode string
		path           any
		wantError      bool
	}{
		{name: "alias", id: "client-alias", path: root},
		{name: "outside", id: "project", path: outside, wantError: true},
		{name: "relative", id: "project", path: ".", wantError: true},
		{name: "malformed", id: "project", path: 42, wantError: true},
		{name: "missing-id", wantError: true},
		{name: "unknown", id: "unknown", wantError: true},
		{name: "empty-needs-no-source", id: "remote", mode: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := plan.Node{ID: "work", Kind: "execute", Executor: "test", WorkspaceMode: tc.mode}
			if tc.path != nil {
				value, _ := json.Marshal(tc.path)
				node.Inputs = map[string]plan.Binding{"workspace": {Expr: &plan.Expr{Op: "literal", Value: value}}}
			}
			p := &plan.Plan{WorkspaceID: tc.id, Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{node}}}}
			_, _ = p.ComputeDigest()
			err := c.AuthorizePlanWorkspace(p, json.RawMessage(`{}`))
			if (err != nil) != tc.wantError {
				t.Fatalf("preflight error = %v", err)
			}
			if err == nil {
				if err := p.VerifyDigest(); err != nil {
					t.Fatal(err)
				}
				if tc.id == "client-alias" && p.WorkspaceID != "project" {
					t.Fatal("alias not canonicalized")
				}
			}
		})
	}
	// Without operator authority, a client path cannot grant a workspace.
	if _, err := (&Coordinator{}).authorizeWorkspace(workers.ExecuteRequest{Inputs: map[string]any{"workspace": root}}); err == nil {
		t.Fatal("client path granted filesystem authority")
	}
}

func TestRemoteDispatchOmitsCoordinatorWorkspace(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	c.Workspaces = workers.WorkspaceRegistry{"project": {WorkerID: "worker", Path: "/coordinator/authorized"}}
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Labels: map[string]string{workers.WorkspaceIdentityLabelPrefix + "project": "true"}, Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		_, _ = c.Execute(workers.ExecuteRequest{WorkspaceID: "project", Context: ctx, RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test"})
	}()
	var assignment *WorkerAssignment
	deadline := time.Now().Add(time.Second)
	for assignment == nil && time.Now().Before(deadline) {
		assignment, err = c.NextAssignment("worker")
		if err != nil {
			t.Fatal(err)
		}
		if assignment == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if assignment == nil {
		t.Fatal("no assignment")
	}
	var payload workers.ExecuteRequest
	if err := json.Unmarshal(assignment.Request, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Workspace != "" {
		t.Fatalf("dispatch included workspace %q", payload.Workspace)
	}
	cancel()
}

func TestWorkspaceWriteIdentityAcrossClients(t *testing.T) {
	root := t.TempDir()
	c := &Coordinator{Workspaces: workers.WorkspaceRegistry{
		"project":     {WorkerID: "local", Path: root, Aliases: []string{"client-one", "client-two"}},
		"independent": {WorkerID: "local", Path: t.TempDir()},
	}}
	var keys []string
	for _, id := range []string{"client-one", "client-two", "independent"} {
		req, err := c.authorizeWorkspace(workers.ExecuteRequest{WorkspaceID: id, WorkspaceMode: "shared-write"})
		if err != nil {
			t.Fatal(err)
		}
		if err := workers.ValidateDispatchWorkspace(req); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, workers.ExclusiveDispatchKeys(req)[0])
	}
	if keys[0] != keys[1] || keys[0] == keys[2] {
		t.Fatalf("workspace keys do not follow resolved authority: %v", keys)
	}
}
