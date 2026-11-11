package api

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
	"path/filepath"
	"testing"
)

func TestRejectMalformedAccountingBeforeAcceptingCompletion(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(s)
	if _, err = c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "act", Executor: "test", State: "running"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(workers.ExecuteRequest{RunID: "run", ActivationID: "act", AttemptID: "attempt", Executor: "test"})
	if err = c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", AttemptID: "attempt", RunID: "run", ActivationID: "act", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("claim %v %v", a, err)
	}
	bad := &contract.Accounting{Revision: 0, Final: true}
	if err = c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded", Accounting: bad}); err == nil {
		stored, _ := s.WorkerResult("attempt")
		t.Errorf("malformed accounting was accepted and sealed as durable worker result: %s", stored)
	}
	stored, err := s.WorkerResult("attempt")
	if err != nil || stored != nil {
		t.Fatalf("rejected result poisoned receipt: %s %v", stored, err)
	}
	valid := &contract.Accounting{Revision: 1, Final: true}
	if err = c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded", Accounting: valid}); err != nil {
		t.Fatalf("corrected completion rejected: %v", err)
	}
	attempts, err := s.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || attempts[0].Accounting == nil || !attempts[0].Accounting.Final {
		t.Fatalf("completion acknowledged before accounting commit: %+v %v", attempts, err)
	}
}
