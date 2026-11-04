package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestTimingCompletionReplayAndRecoveredResult(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "timing.db"))
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
	if err = c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("assignment=%+v err=%v", a, err)
	}
	zero := int64(0)
	completion := WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "failed", ExecutionDurationMS: &zero}
	for i := 0; i < 2; i++ {
		if err = c.Complete("worker", completion); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := s.WorkerResult("attempt")
	if err != nil {
		t.Fatal(err)
	}
	var result workers.ExecuteResult
	if err = json.Unmarshal(raw, &result); err != nil || result.ExecutionDurationMS == nil || *result.ExecutionDurationMS != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	recovered := NewCoordinator(s)
	result, err = recovered.Execute(workers.ExecuteRequest{RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Context: context.Background()})
	if err != nil || result.ExecutionDurationMS == nil || *result.ExecutionDurationMS != 0 {
		t.Fatalf("recovered result=%+v err=%v", result, err)
	}
	negative := int64(-1)
	completion.ExecutionDurationMS = &negative
	if err = recovered.Complete("worker", completion); err == nil {
		t.Fatal("negative worker duration accepted")
	}
}

func TestTimingLateCompletionAfterCancellationOrRecovery(t *testing.T) {
	for _, mode := range []string{"cancellation", "recovery", "retry"} {
		t.Run(mode, func(t *testing.T) {
			s, err := sqlite.Open(filepath.Join(t.TempDir(), "late.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			attempt := state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "activation", RetryNumber: 1, State: "cancelled"}
			if err = s.RecordExecutionAttempt(attempt); err != nil {
				t.Fatal(err)
			}
			c := NewCoordinator(s)
			if _, err = c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
				t.Fatal(err)
			}
			if err = c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			a, err := c.NextAssignment("worker")
			if err != nil || a == nil {
				t.Fatalf("assignment=%+v err=%v", a, err)
			}
			if mode == "recovery" {
				c = NewCoordinator(s)
			} else if err = s.CancelRunWork("run"); err != nil {
				t.Fatal(err)
			}
			if mode == "retry" {
				if _, err = s.ClaimAssignment(state.Assignment{RunID: "run", ActivationID: "activation", WorkerID: "worker", AssignmentID: "new-assignment", AttemptID: "new-attempt"}); err != nil {
					t.Fatal(err)
				}
			}
			ms := int64(75)
			completion := WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "cancelled", ExecutionDurationMS: &ms}
			for i := 0; i < 2; i++ {
				if err = c.Complete("worker", completion); err != nil {
					if _, stale := err.(staleAssignmentError); !stale || mode != "retry" {
						t.Fatal(err)
					}
				}
			}
			// Interpreter replay with no measurement must retain the late receipt.
			if err = s.RecordExecutionAttempt(attempt); err != nil {
				t.Fatal(err)
			}
			attempts, err := s.ListExecutionAttempts("run", "")
			if err != nil || len(attempts) != 1 || attempts[0].ExecutionDurationMS == nil || *attempts[0].ExecutionDurationMS != 75 {
				t.Fatalf("late completion timing lost or doubled: %+v err=%v", attempts, err)
			}
		})
	}
}

// A result must not become observable before its timing receipt is durable.
// Simulate a failure at result publication, then reconstruct the coordinator.
type interruptedTimingStore struct {
	dispatchStore
	err error
}

func (s *interruptedTimingStore) CompleteWorkerAssignment(a state.Assignment, terminal string, now time.Time, raw ...[]byte) (bool, error) {
	return false, s.err
}
func TestTimingReceiptPrecedesResultPublication(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "publication.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "activation", State: "running"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(s)
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	a, err := c.NextAssignment("worker")
	if err != nil || a == nil {
		t.Fatalf("assignment=%+v err=%v", a, err)
	}
	interrupted := errors.New("interrupted result publication")
	c.store = &interruptedTimingStore{dispatchStore: c.store, err: interrupted}
	ms := int64(80)
	completion := WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "failed", ExecutionDurationMS: &ms}
	if err := c.Complete("worker", completion); !errors.Is(err, interrupted) {
		t.Fatalf("completion error=%v", err)
	}
	attempts, err := s.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || attempts[0].ExecutionDurationMS == nil || *attempts[0].ExecutionDurationMS != ms {
		t.Fatalf("receipt not durable before result: %+v err=%v", attempts, err)
	}
	c = NewCoordinator(s)
	if err := c.Complete("worker", completion); err != nil {
		t.Fatal(err)
	}
	attempts, err = s.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || attempts[0].ExecutionDurationMS == nil || *attempts[0].ExecutionDurationMS != ms {
		t.Fatalf("recovery lost or doubled receipt: %+v err=%v", attempts, err)
	}
}
