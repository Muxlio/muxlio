package plan

import (
	"context"
	"time"
)

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
