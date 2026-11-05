package workers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type cancellationTestExecutor func(ExecuteRequest) (ExecuteResult, error)

func (f cancellationTestExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) { return f(req) }

func TestAttemptCancellationSeparatesIntentFromExecutorReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	defer close(release)
	executor := cancellationTestExecutor(func(req ExecuteRequest) (ExecuteResult, error) {
		close(started)
		<-release
		return ExecuteResult{Outputs: map[string]any{"late": true}}, nil
	})
	done := make(chan error, 1)
	go func() {
		result, err := ExecuteAttempt(executor, ExecuteRequest{Context: ctx, OnExecutionReturned: func() { close(returned) }})
		if result.Outputs != nil {
			done <- errors.New("cancelled attempt published output")
			return
		}
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "termination and external effect completion unconfirmed") {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked on external operation")
	}
	select {
	case <-returned:
		t.Fatal("cancellation falsely confirmed executor return")
	default:
	}
}

func TestAttemptDoesNotStartAfterPersistedExecutionDeadline(t *testing.T) {
	deadline := time.Now().Add(-time.Second)
	_, err := ExecuteAttempt(cancellationTestExecutor(func(ExecuteRequest) (ExecuteResult, error) {
		t.Error("expired attempt started")
		return ExecuteResult{}, nil
	}), ExecuteRequest{ExecutionDeadline: &deadline})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}
