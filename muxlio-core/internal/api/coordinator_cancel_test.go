package api

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestHeartbeatDeliversCancelForRevokedAssignment(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "test"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("assignment: %+v %v", a, err)
	}
	hb, err := c.Heartbeat("worker", WorkerHeartbeat{Active: []string{a.AssignmentID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hb.Commands) != 0 {
		t.Fatalf("unexpected commands before cancel: %#v", hb.Commands)
	}
	if err := store.CancelRunWork("run"); err != nil {
		t.Fatal(err)
	}
	hb, err = c.Heartbeat("worker", WorkerHeartbeat{Active: []string{a.AssignmentID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hb.Commands) != 1 {
		t.Fatalf("commands = %#v", hb.Commands)
	}
	raw, _ := json.Marshal(hb.Commands[0])
	if !json.Valid(raw) || string(raw) != `{"cancel":{"assignmentId":"`+a.AssignmentID+`"}}` {
		t.Fatalf("cancel command = %s", raw)
	}
}

func TestStaleEpochCompletionStoredAsDiagnostic(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "epoch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "test"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("assignment: %+v %v", a, err)
	}
	epoch := a.CoordinatorEpoch
	newEpoch, err := store.StartCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	c.epoch = newEpoch
	if newEpoch != epoch+1 {
		t.Fatalf("epoch = %d want %d", newEpoch, epoch+1)
	}
	err = c.Complete("worker", WorkerCompletion{
		AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID,
		CoordinatorEpoch: epoch, Generation: a.Generation, State: "succeeded", Outputs: map[string]any{"answer": "late"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.WorkerResult(a.AttemptID)
	if err != nil || len(result) != 0 {
		t.Fatalf("worker result should be empty: %q %v", result, err)
	}
	diagnostic, err := store.WorkerDiagnostic(a.AttemptID)
	if err != nil || len(diagnostic) == 0 {
		t.Fatalf("diagnostic missing: %q %v", diagnostic, err)
	}
	assignments, err := store.ListAssignments("run")
	if err != nil || len(assignments) != 1 {
		t.Fatal(err)
	}
	if assignments[0].State != "cancelled" {
		t.Fatalf("assignment state = %q", assignments[0].State)
	}
}

func TestHeartbeatDeliversPauseForPausedRun(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "pause.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "test"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("assignment: %+v %v", a, err)
	}
	if err := store.SetRunState("run", "paused"); err != nil {
		t.Fatal(err)
	}
	hb, err := c.Heartbeat("worker", WorkerHeartbeat{Active: []string{a.AssignmentID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hb.Commands) != 1 {
		t.Fatalf("commands = %#v", hb.Commands)
	}
	raw, _ := json.Marshal(hb.Commands[0])
	if string(raw) != `{"pause":{"assignmentId":"`+a.AssignmentID+`"}}` {
		t.Fatalf("pause command = %s", raw)
	}
}

func TestRevokedAssignmentAcceptsCancelledCompletion(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "revoked.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "test"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("assignment: %+v %v", a, err)
	}
	if err := store.CancelRunWork("run"); err != nil {
		t.Fatal(err)
	}
	err = c.Complete("worker", WorkerCompletion{
		AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID,
		CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "cancelled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := store.WorkerResult(a.AttemptID); len(raw) != 0 {
		t.Fatalf("unexpected worker result %q", raw)
	}
	assignments, err := store.ListAssignments("run")
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || assignments[0].State != "cancelled" {
		t.Fatalf("assignments = %+v", assignments)
	}
	// Idempotent retry.
	if err := c.Complete("worker", WorkerCompletion{
		AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID,
		CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "cancelled",
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
}
