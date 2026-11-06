package plan

import (
	"context"
	"time"
)

// telemetryContext retains the run span without changing cancellation for
// callers that execute a subgraph directly.
func (e *Engine) telemetryContext() context.Context {
	if e.traceContext != nil {
		return e.traceContext
	}
	return e.context()
}

func (e *Engine) context() context.Context {
	e.ctxOnce.Do(func() {
		parent := e.Context
		if parent == nil {
			parent = context.Background()
		}
		e.ctx, e.cancel = context.WithCancel(parent)
	})
	return e.ctx
}

// applyRunDeadline uses the persisted creation time, so pause, recovery and
// explicit retry cannot extend an end-to-end deadline.
func (e *Engine) applyRunDeadline(deadline time.Time) {
	e.ctxOnce.Do(func() {
		parent := e.Context
		if parent == nil {
			parent = context.Background()
		}
		e.ctx, e.cancel = context.WithDeadline(parent, deadline)
	})
}

// Pause stops the interpreter at its next operation boundary. An operation
// already running is allowed to finish because external effects cannot be
// suspended safely by the coordinator.
func (e *Engine) Pause() {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()
	if !e.paused {
		e.paused = true
		e.resumeCh = make(chan struct{})
	}
}

// Resume releases an interpreter stopped at an operation boundary.
func (e *Engine) Resume() {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()
	if e.paused {
		e.paused = false
		close(e.resumeCh)
		e.resumeCh = nil
	}
}

// Cancel interrupts local execution where the executor supports cancellation.
func (e *Engine) Cancel() {
	e.controlMu.Lock()
	e.cancelRequested = true
	e.controlMu.Unlock()
	e.context()
	if e.cancel != nil {
		e.cancel()
	}
}

func (e *Engine) checkpoint() error {
	ctx := e.context()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.controlMu.Lock()
		if !e.paused {
			e.controlMu.Unlock()
			return nil
		}
		resumeCh := e.resumeCh
		e.controlMu.Unlock()
		select {
		case <-resumeCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (e *Engine) wait(delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-e.context().Done():
		return e.context().Err()
	}
}

// Parent service shutdown interrupts execution for durable recovery. Explicit
// scope cancellation and the run's deadline instead commit cancellation intent.
func (e *Engine) scopeInterrupted() bool {
	e.controlMu.Lock()
	requested := e.cancelRequested
	e.controlMu.Unlock()
	return requested || e.Context == nil || e.Context.Err() == nil
}
