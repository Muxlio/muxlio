package plan

import (
	"errors"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
	"strings"
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
	for _, seconds := range []*float64{nil, queueSeconds(0), queueSeconds(2), queueSeconds(600)} {
		x := &queueFallbackExecutor{t: t}
		e := NewEngine(&Plan{}, x)
		x.engine = e
		node := &Node{ID: "node", Executor: "primary", FallbackExecutors: []string{"fallback"}, Retry: &RetryPolicy{MaxAttempts: 2, Effect: "retryable"}}
		node.QueueTimeoutSeconds = seconds
		if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err != nil {
			t.Fatal(err)
		}
		if len(x.calls) != 2 || x.calls[1].Executor != "fallback" {
			t.Fatalf("calls=%+v", x.calls)
		}
		want := workers.DefaultQueueTimeoutSeconds
		if seconds != nil && *seconds > 0 {
			want = *seconds
		}
		for _, call := range x.calls {
			if call.QueueTimeoutSeconds == nil || *call.QueueTimeoutSeconds != want {
				t.Fatalf("queue timeout=%v, want %v", call.QueueTimeoutSeconds, want)
			}
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

func queueSeconds(value float64) *float64 { return &value }

func TestQueueTimeoutWithoutFallbackIsBounded(t *testing.T) {
	x := &queueFallbackExecutor{t: t}
	e := NewEngine(&Plan{}, x)
	x.engine = e
	_, err := e.executeExecute("run", &Node{ID: "node", Executor: "primary", Retry: &RetryPolicy{MaxAttempts: 2, Effect: "retryable"}}, nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "queue timeout: no worker admitted") || len(x.calls) != 2 {
		t.Fatalf("unadmitted attempts must exhaust bounded retries: calls=%d err=%v", len(x.calls), err)
	}
	for _, call := range x.calls {
		if call.QueueTimeoutSeconds == nil || *call.QueueTimeoutSeconds != 180 {
			t.Fatalf("missing three-minute queue timeout: %+v", call.QueueTimeoutSeconds)
		}
	}
	attempts, err := e.Store().ListExecutionAttempts("run", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range attempts {
		if attempt.State != "failed" || attempt.AwaitReconciliationReason != "" {
			t.Fatalf("queue expiry should fail, not await execution reconciliation: %+v", attempt)
		}
	}
}
