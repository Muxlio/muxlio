package plan

import (
	"errors"
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
	for _, effect := range []string{"manual", "none", "reconcile", "idempotency-key"} {
		executor := workers.NewFake().ByExecutor("primary", workers.ExecuteResult{Error: "failed"})
		engine := NewEngine(&Plan{}, executor)
		_, err := engine.executeExecute("run", &Node{ID: "node", Executor: "primary", FallbackExecutors: []string{"fallback"}, Retry: &RetryPolicy{MaxAttempts: 3, Effect: effect}}, nil, &EvalContext{})
		if err == nil || len(executor.Calls()) != 1 {
			t.Fatalf("effect %s retried: calls=%d err=%v", effect, len(executor.Calls()), err)
		}
	}
}
