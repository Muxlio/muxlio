package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func enqueueRecoverableAny(t *testing.T, c *Coordinator, deadline time.Time, req workers.ExecuteRequest) workers.ExecuteRequest {
	t.Helper()
	s := c.store.(state.Store)
	if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "step", RunID: "run", NodeID: "step", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "step", Executor: "any", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	queued := deadline.Add(-10 * time.Second)
	req.RunID, req.ActivationID, req.AttemptID, req.Executor = "run", "step", "attempt", "any"
	req.QueuedAt, req.QueueDeadline = &queued, &deadline
	if req.WorkspaceMode == "" {
		req.WorkspaceMode = "empty"
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "attempt", RunID: "run", ActivationID: "step", AttemptID: "attempt", Executor: "any", Request: raw}); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestAnyAgentQueuedRecoverySurvivesOfflineWorkers(t *testing.T) {
	s := newSQLiteStoreForPreflight(t)
	c := NewCoordinator(s)
	deadline := time.Now().Add(5 * time.Second)
	req := enqueueRecoverableAny(t, c, deadline, workers.ExecuteRequest{})
	// Coordinator shutdown suspends the dispatch, preserving its original wait.
	recovered := NewCoordinator(s)
	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	req.Context, req.LifetimeContext = ctx, ctx
	_, err := recovered.Execute(req)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("offline recovery failed instead of waiting: %v", err)
	}
	persisted, err := recovered.assignmentRequest("attempt")
	if err != nil || persisted == nil || persisted.QueueDeadline == nil || !persisted.QueueDeadline.Equal(deadline) {
		t.Fatalf("queued dispatch lost its deadline: %+v, %v", persisted, err)
	}
	if persisted.Executor != "any" {
		t.Fatalf("offline dispatch selected an agent: %s", persisted.Executor)
	}
	// A later worker can complete the same attempt after another restart.
	recovered = NewCoordinator(s)
	ctx, stop = context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	req.Context, req.LifetimeContext = ctx, ctx
	done := make(chan error, 1)
	go func() {
		result, err := recovered.Execute(req)
		if err == nil && result.Outputs["result"] != "done" {
			err = errors.New("missing recovered result")
		}
		done <- err
	}()
	registerWorker(t, s, "worker", []state.WorkerCapability{readyCapability("agent/test", "1")}, nil)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var assignment *WorkerAssignment
	for assignment == nil {
		select {
		case err := <-done:
			t.Fatalf("recovery ended before assignment: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			assignment, err = recovered.NextAssignmentForInstance("worker", "worker-inst")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var dispatched workers.ExecuteRequest
	if err := json.Unmarshal(assignment.Request, &dispatched); err != nil {
		t.Fatal(err)
	}
	if assignment.AttemptID != "attempt" || dispatched.QueueDeadline == nil || !dispatched.QueueDeadline.Equal(deadline) {
		t.Fatalf("recovery changed dispatch identity or wait: %+v", dispatched)
	}
	if err := recovered.Complete("worker", WorkerCompletion{AssignmentID: assignment.AssignmentID, RunID: "run", ActivationID: "step", AttemptID: "attempt", CoordinatorEpoch: assignment.CoordinatorEpoch, Generation: assignment.Generation, State: "succeeded", Outputs: map[string]any{"result": "done"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Cached completion also remains readable when the worker is offline.
	if err := recovered.store.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{readyCapability("agent/test", "1")}, LastSeen: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if result, err := recovered.Execute(req); err != nil || result.Outputs["result"] != "done" {
		t.Fatalf("offline completion could not be read: %+v %v", result, err)
	}
}

func TestAnyAgentQueuedRecoveryHonorsOriginalExpiry(t *testing.T) {
	for _, mode := range []string{"plain", "session", "remote-workspace"} {
		t.Run(mode, func(t *testing.T) {
			s := newSQLiteStoreForPreflight(t)
			c := NewCoordinator(s)
			req := workers.ExecuteRequest{}
			if mode == "session" {
				req.Session = &contract.SessionRequest{Mode: "create"}
			}
			if mode == "remote-workspace" {
				c.Workspaces = workers.WorkspaceRegistry{"project": {WorkerID: "offline", Path: "/offline/project"}}
				req.WorkspaceID, req.WorkspaceWorkerID, req.WorkspaceMode = "project", "offline", "snapshot"
			}
			req = enqueueRecoverableAny(t, c, time.Now().Add(-time.Second), req)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			req.Context = ctx
			_, err := c.Execute(req)
			var timeout workers.QueueTimeoutError
			if !errors.As(err, &timeout) {
				t.Fatalf("queued recovery bypassed expiry: %v", err)
			}
		})
	}
}
