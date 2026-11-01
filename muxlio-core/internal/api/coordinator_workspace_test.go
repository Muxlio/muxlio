package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

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
	c.Workspace = filepath.Join(t.TempDir(), "coordinator-authorized")
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = c.Execute(workers.ExecuteRequest{Context: ctx, RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test"})
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
