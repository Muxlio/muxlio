package api

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestCompleteDoesNotRecordAvailabilityForStaleAssignment(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "w1", Capacity: 1, Capabilities: []WorkerCapability{{Name: "agent/x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "w", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "act", RunID: "run", NodeID: "step", State: "running"}); err != nil {
		t.Fatal(err)
	}
	req := workers.ExecuteRequest{Executor: "agent/x", RunID: "run", ActivationID: "act", AttemptID: "attempt"}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "attempt", RunID: "run", ActivationID: "act", RetryNumber: 1, Executor: "agent/x", State: "queued", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueDispatch(state.Dispatch{
		AssignmentID: "attempt", RunID: "run", ActivationID: "act", AttemptID: "attempt", Executor: "agent/x", Request: raw,
	}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("w1")
	if err != nil || a == nil {
		t.Fatalf("claim: %+v %v", a, err)
	}

	assertNoAvailability := func(t *testing.T) {
		t.Helper()
		blocked, lookupErr := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: "w1", Executor: "agent/x"})
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if blocked != nil {
			t.Fatalf("rejected completion must not record availability: %+v", blocked)
		}
	}

	// Epoch + generation mismatch: hard stale rejection.
	err = c.Complete("w1", WorkerCompletion{
		AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID,
		CoordinatorEpoch: a.CoordinatorEpoch + 99, Generation: a.Generation + 7, State: "failed",
		Availability: &contract.Availability{Reason: contract.AvailabilityQuotaExhausted, Message: "quota"},
		Error:        &WorkerError{Code: "execution_failed", Message: "quota"},
	})
	var stale staleAssignmentError
	if !errors.As(err, &stale) {
		t.Fatalf("expected stale assignment error, got %v", err)
	}
	assertNoAvailability(t)

	// Wrong generation with current epoch: CompleteWorkerAssignment rejects; diagnostic only.
	if err := c.Complete("w1", WorkerCompletion{
		AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID,
		CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation + 3, State: "failed",
		Availability: &contract.Availability{Reason: contract.AvailabilityQuotaExhausted, Message: "quota"},
		Error:        &WorkerError{Code: "execution_failed", Message: "quota"},
	}); err != nil {
		t.Fatalf("diagnostic generation mismatch: %v", err)
	}
	assertNoAvailability(t)
}

func TestCompleteRecordsAvailabilityOnlyWhenAccepted(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "w1", Capacity: 1, Capabilities: []WorkerCapability{{Name: "agent/x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "w", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "act", RunID: "run", NodeID: "step", State: "running"}); err != nil {
		t.Fatal(err)
	}
	req := workers.ExecuteRequest{Executor: "agent/x", RunID: "run", ActivationID: "act", AttemptID: "attempt"}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "attempt", RunID: "run", ActivationID: "act", RetryNumber: 1, Executor: "agent/x", State: "queued", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueDispatch(state.Dispatch{
		AssignmentID: "attempt", RunID: "run", ActivationID: "act", AttemptID: "attempt", Executor: "agent/x", Request: raw,
	}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("w1")
	if err != nil || a == nil {
		t.Fatalf("claim: %+v %v", a, err)
	}
	if err := c.Complete("w1", WorkerCompletion{
		AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID,
		CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "failed",
		Availability: &contract.Availability{Reason: contract.AvailabilityQuotaExhausted, Message: "quota"},
		Error:        &WorkerError{Code: "execution_failed", Message: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	blocked, lookupErr := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: "w1", Executor: "agent/x"})
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	if blocked == nil || blocked.Reason != contract.AvailabilityQuotaExhausted {
		t.Fatalf("accepted completion must record availability: %+v", blocked)
	}
}
