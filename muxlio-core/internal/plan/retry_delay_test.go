package plan

import (
	"errors"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
	"testing"
	"time"
)

func TestRetryDelayThrottlingAndBounds(t *testing.T) {
	if got := retryDelay(&RetryPolicy{}, 1, errors.New("HTTP 429 rate limit")); got != time.Second {
		t.Fatalf("throttle delay=%v", got)
	}
	if got := retryDelay(&RetryPolicy{BackoffSeconds: 2, MaxBackoffSeconds: 5}, 20, errors.New("failed")); got != 5*time.Second {
		t.Fatalf("bounded delay=%v", got)
	}
	if got := retryDelay(&RetryPolicy{}, 1, errors.New("ordinary failure")); got != 0 {
		t.Fatalf("unexpected delay=%v", got)
	}
}

type uncertainExecutor struct{ calls int }

func (e *uncertainExecutor) Execute(workers.ExecuteRequest) (workers.ExecuteResult, error) {
	e.calls++
	return workers.ExecuteResult{}, workers.UncertainExecutionError{}
}

func TestUncertainExecutionNeverRetriesOrFallsBack(t *testing.T) {
	executor := &uncertainExecutor{}
	engine := NewEngine(&Plan{}, executor)
	_, err := engine.executeExecute("run", &Node{ID: "node", Executor: "primary", FallbackExecutors: []string{"fallback"}, Retry: &RetryPolicy{MaxAttempts: 3, Effect: "retryable"}}, nil, &EvalContext{})
	var uncertain workers.UncertainExecutionError
	if !errors.As(err, &uncertain) || executor.calls != 1 {
		t.Fatalf("uncertain execution retried: calls=%d err=%v", executor.calls, err)
	}
}

func TestEffectPoliciesDoNotPretendToEnforceReconciliation(t *testing.T) {
	for _, effect := range []string{"manual", "none", "reconcile"} {
		executor := workers.NewFake().ByExecutor("primary", workers.ExecuteResult{Error: "failed"})
		engine := NewEngine(&Plan{}, executor)
		_, err := engine.executeExecute("run", &Node{ID: "node", Executor: "primary", FallbackExecutors: []string{"fallback"}, Retry: &RetryPolicy{MaxAttempts: 3, Effect: effect}}, nil, &EvalContext{})
		if err == nil || len(executor.Calls()) != 1 {
			t.Fatalf("effect %s retried: calls=%d err=%v", effect, len(executor.Calls()), err)
		}
		if got := executor.Calls()[0].Effect; got != effect {
			t.Fatalf("executor effect = %q, want %q", got, effect)
		}
	}
}

type queueFallbackExecutor struct {
	engine *Engine
	t      *testing.T
	calls  []workers.ExecuteRequest
}

func (x *queueFallbackExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return x.ExecuteQueued(req)
}
func (x *queueFallbackExecutor) ExecuteQueued(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	x.calls = append(x.calls, req)
	attempts, err := x.engine.Store().ListExecutionAttempts(req.RunID, req.ActivationID)
	if err != nil || len(attempts) == 0 || attempts[len(attempts)-1].State != "queued" {
		x.t.Fatalf("admission must be queued: %+v %v", attempts, err)
	}
	if req.Executor == "primary" {
		return workers.ExecuteResult{}, workers.QueueTimeoutError{Deadline: time.Now()}
	}
	return workers.ExecuteResult{}, nil
}
func TestQueueTimeoutReachesFallback(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		x := &queueFallbackExecutor{t: t}
		e := NewEngine(&Plan{}, x)
		x.engine = e
		node := &Node{ID: "node", Executor: "primary", FallbackExecutors: []string{"fallback"}, Retry: &RetryPolicy{MaxAttempts: 2, Effect: "retryable"}}
		if explicit {
			seconds := 2.0
			node.QueueTimeoutSeconds = &seconds
		}
		if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err != nil {
			t.Fatal(err)
		}
		if len(x.calls) != 2 || x.calls[1].Executor != "fallback" {
			t.Fatalf("calls=%+v", x.calls)
		}
		want := 60.0
		if explicit {
			want = 2
		}
		if x.calls[0].QueueTimeoutSeconds == nil || *x.calls[0].QueueTimeoutSeconds != want {
			t.Fatalf("queue timeout=%v", x.calls[0].QueueTimeoutSeconds)
		}
		attempts, _ := e.Store().ListExecutionAttempts("run", "")
		if len(attempts) != 2 || attempts[0].State != "failed" || attempts[0].AwaitReconciliationReason != "" || attempts[1].State != "succeeded" {
			t.Fatalf("attempts=%+v", attempts)
		}
	}
}

func TestReadmittingAttemptPreservesClaimedState(t *testing.T) {
	e := NewEngine(&Plan{}, workers.NewFake())
	first := state.ExecutionAttempt{ID: "attempt", RunID: "run", State: "running"}
	if err := e.admitAttempt(first); err != nil {
		t.Fatal(err)
	}
	first.State = "queued"
	if err := e.admitAttempt(first); err != nil {
		t.Fatal(err)
	}
	attempts, err := e.Store().ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || attempts[0].State != "running" {
		t.Fatalf("reattachment regressed admission: %+v %v", attempts, err)
	}
}
