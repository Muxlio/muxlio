package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestAnyAgentWorkflowRetryRespectsFailureAndKeyAffinity(t *testing.T) {
	for _, effect := range []string{"retryable", "idempotent-with-key"} {
		t.Run(effect, func(t *testing.T) {
			s := newSQLiteStoreForPreflight(t)
			c := NewCoordinator(s)
			caps := []state.WorkerCapability{readyCapability("agent/a", "1"), readyCapability("agent/b", "1")}
			for i := range caps {
				caps[i].Contract = &contract.AdapterManifest{Capabilities: map[string]bool{contract.CapabilityIdempotencyKey: true}}
			}
			registerWorker(t, s, "worker", caps, nil)
			p := minimalPlan("any")
			p.Subgraphs["root"].Nodes[0].Retry = &plan.RetryPolicy{Effect: effect, MaxAttempts: 2}
			e := plan.NewEngineWithStore(&p, c, s)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			e.Context = ctx
			id, err := e.CreateRun(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- e.Run(id) }()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			want := []string{"agent/a", "agent/b"}
			if effect == "idempotent-with-key" {
				want[1] = "agent/a"
			}
			for index, name := range want {
				var a *WorkerAssignment
				for a == nil {
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case err := <-done:
						t.Fatalf("workflow ended before retry: %v", err)
					case <-ticker.C:
						a, err = c.NextAssignmentForInstance("worker", "worker-inst")
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				var req workers.ExecuteRequest
				if err := json.Unmarshal(a.Request, &req); err != nil {
					t.Fatal(err)
				}
				if req.Executor != name {
					t.Fatalf("attempt %d selected %s, want %s", index+1, req.Executor, name)
				}
				status := "succeeded"
				if index == 0 {
					status = "failed"
				}
				if err := c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: id, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: status}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			attempts, err := s.ListExecutionAttempts(id, "")
			if err != nil || len(attempts) != 2 || attempts[0].Executor != want[0] || attempts[1].Executor != want[1] || attempts[1].State != "succeeded" {
				t.Fatalf("incorrect persisted identities: %+v %v", attempts, err)
			}
		})
	}
}

func TestAnyAgentWorkflowDispatchesConcreteAgents(t *testing.T) {
	s := newSQLiteStoreForPreflight(t)
	c := NewCoordinator(s)
	registerWorker(t, s, "worker", []state.WorkerCapability{readyCapability("agent/a", "1"), readyCapability("agent/b", "1")}, nil)
	var p plan.Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"automatic","root":"root","subgraphs":{"root":{"nodes":[{"id":"first","kind":"execute","executor":"any","task":"Return result","outputs":{"result":{}}},{"id":"second","kind":"execute","executor":"agent/any","task":"Return result","inputs":{"previous":{"ref":{"node":"first","port":"result"}}},"outputs":{"result":{}}}]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	e := plan.NewEngineWithStore(&p, c, s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e.Context = ctx
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(id) }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for _, want := range []string{"agent/a", "agent/b"} {
		var assignment *WorkerAssignment
		for assignment == nil {
			select {
			case <-ctx.Done():
				t.Fatal("workflow did not dispatch", ctx.Err())
			case err := <-done:
				t.Fatalf("workflow ended before dispatch: %v", err)
			case <-ticker.C:
				assignment, err = c.NextAssignmentForInstance("worker", "worker-inst")
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		var req workers.ExecuteRequest
		if err := json.Unmarshal(assignment.Request, &req); err != nil {
			t.Fatal(err)
		}
		if req.Executor != want {
			t.Fatalf("dispatched %q, want %q", req.Executor, want)
		}
		attempts, err := s.ListExecutionAttempts(id, assignment.ActivationID)
		if err != nil || len(attempts) != 1 || attempts[0].Executor != want {
			t.Fatalf("selection not durable: %+v %v", attempts, err)
		}
		if err := c.Complete("worker", WorkerCompletion{AssignmentID: assignment.AssignmentID, RunID: id, ActivationID: assignment.ActivationID, AttemptID: assignment.AttemptID, CoordinatorEpoch: assignment.CoordinatorEpoch, Generation: assignment.Generation, State: "succeeded", Outputs: map[string]any{"result": "done"}}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestAnyAgentPreflightAndSelectionAgree(t *testing.T) {
	s := newSQLiteStoreForPreflight(t)
	c := NewCoordinator(s)
	registerWorker(t, s, "worker", []state.WorkerCapability{unreadyCapability("agent/a"), readyCapability("agent/b", "1"), readyCapability("command", "1")}, nil)
	for _, selector := range []string{"any", "agent/any"} {
		p := minimalPlan(selector)
		p.Subgraphs["root"].Nodes[0].Params = json.RawMessage(`{"executionPolicy":{"required":true,"allowedCapabilities":["agent/b"]}}`)
		preflight := PreflightRun(&p, nil, s)
		if !preflight.Feasible {
			t.Fatalf("preflight rejected selector: %+v", preflight)
		}
		req := workers.ExecuteRequest{Executor: selector, Policy: &workers.ExecutionPolicy{Required: true, AllowedCapabilities: []string{"agent/b"}}}
		if err := c.prepareAgentSelection(&req, false); err != nil {
			t.Fatalf("selection validation: %v", err)
		}
	}
	if err := c.store.RegisterWorker(state.Worker{ID: "worker", Draining: true, Capacity: 4, LastSeen: time.Now(), Capabilities: []state.WorkerCapability{readyCapability("agent/b", "1")}}); err != nil {
		t.Fatal(err)
	}
	req := workers.ExecuteRequest{Executor: "any"}
	if err := c.prepareAgentSelection(&req, false); err == nil {
		t.Fatal("selected draining worker")
	}
	p := minimalPlan("any")
	if result := PreflightRun(&p, nil, s); result.Feasible {
		t.Fatal("preflight accepted draining worker")
	}
}
