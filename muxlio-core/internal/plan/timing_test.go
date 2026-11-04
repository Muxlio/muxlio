package plan

import (
	"context"
	"errors"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type timedResultExecutor struct {
	calls  int
	cancel bool
}

func (x *timedResultExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	x.calls++
	ms := int64(40 * x.calls)
	result := workers.ExecuteResult{ExecutionDurationMS: &ms}
	if x.cancel {
		return result, context.Canceled
	}
	if x.calls == 1 {
		return result, errors.New("failed")
	}
	return result, nil
}

func TestTimingRetainedAcrossRunRetryAndEngineReconstruction(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "r:task", RunID: "r", NodeID: "task", State: ActRunning}); err != nil {
		t.Fatal(err)
	}
	x := &timedResultExecutor{}
	node := &Node{ID: "task", Executor: "test"}
	e := NewEngineWithStore(&Plan{}, x, s)
	if _, err := e.executeExecute("r", node, nil, &EvalContext{}); err == nil {
		t.Fatal("expected failure")
	}
	if err := s.SetRunError("r", errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivationError("r:task", errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryRun("r"); err != nil {
		t.Fatal(err)
	}
	// The offset must survive reconstruction, rather than live only on Engine.
	e = NewEngineWithStore(&Plan{}, x, s)
	if _, err := e.executeExecute("r", node, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("r", "")
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	if attempts[0].ID == attempts[1].ID || attempts[1].RetryNumber != 2 || *attempts[0].ExecutionDurationMS+*attempts[1].ExecutionDurationMS != 120 {
		t.Fatalf("lost retry timing: %+v", attempts)
	}
}

func TestTimingIncludesFailedAutomaticRetryAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		s := state.New()
		x := &timedResultExecutor{cancel: cancelled}
		e := NewEngineWithStore(&Plan{}, x, s)
		_, err := e.executeExecute("r", &Node{ID: "task", Executor: "test", Retry: &RetryPolicy{MaxAttempts: 2}}, nil, &EvalContext{})
		attempts, _ := s.ListExecutionAttempts("r", "")
		if cancelled {
			if !errors.Is(err, context.Canceled) || len(attempts) != 1 || attempts[0].State != "cancelled" || attempts[0].ExecutionDurationMS == nil || *attempts[0].ExecutionDurationMS != 40 {
				t.Fatalf("cancellation timing lost: %+v err=%v", attempts, err)
			}
		} else if err != nil || len(attempts) != 2 || attempts[0].State != "failed" || *attempts[0].ExecutionDurationMS+*attempts[1].ExecutionDurationMS != 120 {
			t.Fatalf("retry timing lost: %+v err=%v", attempts, err)
		}
	}
}
