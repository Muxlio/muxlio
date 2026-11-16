package plan

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type countingExecutor struct {
	ids []string
}

func TestExplicitRetryAfterCancelledAttempt(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "sqlite"}[persistent], func(t *testing.T) {
			var s state.Store = state.New()
			if persistent {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "retry.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				s = db
			}
			id := activationID("run", nil, "node")
			if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateActivation(&state.Activation{ID: id, RunID: "run", NodeID: "node", State: ActCancelled}); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: id + "-attempt-1", RunID: "run", ActivationID: id, RetryNumber: 1, State: "cancelled", Failure: "cancelled", Effect: "pure"}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetRunState("run", RunCancelled); err != nil {
				t.Fatal(err)
			}
			if err := s.RetryRun("run"); err != nil {
				t.Fatal(err)
			}
			x := &countingExecutor{}
			e := NewEngineWithStore(&Plan{}, x, s)
			if _, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{Effect: "pure"}}, nil, &EvalContext{}); err != nil {
				t.Fatal(err)
			}
			if len(x.ids) != 1 || x.ids[0] != id+"-attempt-2" {
				t.Fatalf("dispatches = %v", x.ids)
			}
			attempts, err := s.ListExecutionAttempts("run", id)
			if err != nil || len(attempts) != 2 || attempts[0].State != "cancelled" {
				t.Fatalf("attempts = %+v: %v", attempts, err)
			}
		})
	}
}

func (c *countingExecutor) Execute(r workers.ExecuteRequest) (workers.ExecuteResult, error) {
	c.ids = append(c.ids, r.AttemptID)
	return workers.ExecuteResult{}, nil
}

func TestRecordedFailureIsNotReexecuted(t *testing.T) {
	s := state.New()
	id := activationID("run", nil, "node")
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: id + "-attempt-1", RunID: "run", ActivationID: id, RetryNumber: 1,
		State: "failed", Failure: "build failed", Executor: "test", Effect: "retryable",
	}); err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	e := NewEngineWithStore(&Plan{}, exec, s)
	_, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{MaxAttempts: 1, Effect: "retryable"}}, nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("err = %v", err)
	}
	if len(exec.ids) != 0 {
		t.Fatalf("re-executed recorded failure: %v", exec.ids)
	}
	attempts, err := s.ListExecutionAttempts("run", id)
	if err != nil || len(attempts) != 1 || attempts[0].Failure != "build failed" {
		t.Fatalf("stored failure changed: %+v %v", attempts, err)
	}
}

func TestRetryableRecoveryUsesTheNextAttemptID(t *testing.T) {
	s := state.New()
	id := activationID("run", nil, "node")
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: id + "-attempt-1", RunID: "run", ActivationID: id, RetryNumber: 1,
		State: "failed", Failure: "again", Executor: "test", Effect: "retryable",
	}); err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	e := NewEngineWithStore(&Plan{}, exec, s)
	if _, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{MaxAttempts: 3, Effect: "retryable"}}, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	if len(exec.ids) != 1 || !strings.HasSuffix(exec.ids[0], "-attempt-2") {
		t.Fatalf("attempts = %v", exec.ids)
	}
	attempts, err := s.ListExecutionAttempts("run", id)
	if err != nil || len(attempts) != 2 || attempts[0].Failure != "again" {
		t.Fatalf("original failure was overwritten: %+v %v", attempts, err)
	}
}

func TestCancelledAttemptIsNotRedispatched(t *testing.T) {
	s := state.New()
	id := activationID("run", nil, "node")
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: id + "-attempt-1", RunID: "run", ActivationID: id, RetryNumber: 1,
		State: "cancelled", Failure: "run deadline exceeded", Executor: "test",
	}); err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	e := NewEngineWithStore(&Plan{}, exec, s)
	_, err := e.executeExecute("run", &Node{ID: "node", Executor: "test", Retry: &RetryPolicy{MaxAttempts: 3, Effect: "retryable"}}, nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "run deadline exceeded") {
		t.Fatalf("err = %v", err)
	}
	if len(exec.ids) != 0 {
		t.Fatalf("cancelled attempt was dispatched again: %v", exec.ids)
	}
}
