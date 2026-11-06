package workers

import (
	"context"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/telemetry"
)

// QueuedExecutor owns admission timing; execution timeout starts after admission.
type QueuedExecutor interface {
	ExecuteQueued(ExecuteRequest) (ExecuteResult, error)
}

// ExecuteAttempt propagates cancellation without claiming that an executor's
// return proves an external effect was rolled back or even stopped.
func ExecuteAttempt(executor Executor, req ExecuteRequest) (result ExecuteResult, executionErr error) {
	ctx, end := telemetry.Start(req.Context, "muxlio.attempt", telemetry.AttemptAttributes(req.RunID, req.ActivationID, req.AttemptID, req.Executor)...)
	req.Context = ctx
	defer func() {
		telemetry.Usage(ctx, result.Usage)
		end(executionErr != nil || result.Error != "")
	}()
	if queued, ok := executor.(QueuedExecutor); ok {
		return queued.ExecuteQueued(req)
	}
	cancel := func() {}
	if req.Deadline != nil {
		ctx, cancel = context.WithDeadline(ctx, *req.Deadline)
	}
	defer cancel()
	if req.ExecutionDeadline != nil {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, *req.ExecutionDeadline)
		defer stop()
	}
	if req.TimeoutSeconds != nil && *req.TimeoutSeconds > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, time.Duration(*req.TimeoutSeconds*float64(time.Second)))
		defer stop()
	}
	req.Context = ctx
	if err := ctx.Err(); err != nil {
		return ExecuteResult{}, err
	}
	type outcome struct {
		result ExecuteResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		started := time.Now()
		result, err := executor.Execute(req)
		telemetry.Duration(ctx, "muxlio.execution.duration", time.Since(started), req.Executor)
		if req.OnExecutionReturned != nil {
			req.OnExecutionReturned()
		}
		done <- outcome{result, err}
	}()
	select {
	case result := <-done:
		// Cancellation wins when both facts are observable; late output is fenced.
		if err := ctx.Err(); err != nil {
			result.result.Outputs = nil
			return result.result, fmt.Errorf("execution timeout or cancellation: %w", err)
		}
		return result.result, result.err
	case <-ctx.Done():
		return ExecuteResult{}, fmt.Errorf("execution timeout or cancellation; executor termination and external effect completion unconfirmed: %w", ctx.Err())
	}
}
