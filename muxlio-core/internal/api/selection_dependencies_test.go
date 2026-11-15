package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func runSelectionWorkflow(t *testing.T, s state.Store, c *Coordinator, p *plan.Plan) []string {
	t.Helper()
	e := plan.NewEngineWithStore(p, c, s)
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
	var names []string
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return names
		case <-ticker.C:
			a, err := c.NextAssignmentForInstance("worker", "worker-inst")
			if err != nil {
				t.Fatal(err)
			}
			if a == nil {
				continue
			}
			var req workers.ExecuteRequest
			if err := json.Unmarshal(a.Request, &req); err != nil {
				t.Fatal(err)
			}
			names = append(names, req.Executor)
			if err := c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: id, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded", Outputs: map[string]any{"result": "done"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAnyAgentDiversityFollowsChildOutputProducer(t *testing.T) {
	s := newSQLiteStoreForPreflight(t)
	c := NewCoordinator(s)
	registerWorker(t, s, "worker", []state.WorkerCapability{readyCapability("agent/a", "1")}, nil)
	warmup := minimalPlan("any")
	if names := runSelectionWorkflow(t, s, c, &warmup); len(names) != 1 || names[0] != "agent/a" {
		t.Fatalf("warmup: %v", names)
	}
	registerWorker(t, s, "worker", []state.WorkerCapability{readyCapability("agent/a", "1"), readyCapability("agent/b", "1")}, nil)
	var p plan.Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"child-review","root":"root","subgraphs":{"root":{"nodes":[{"id":"generate","kind":"child","childRef":"generation","maxDepth":1},{"id":"project","kind":"project","inputs":{"result":{"ref":{"node":"generate","port":"result"}}}},{"id":"review","kind":"execute","executor":"any","inputs":{"text":{"ref":{"node":"project","port":"result"}}}}]},"generation":{"nodes":[{"id":"writer","kind":"execute","executor":"agent/b","outputs":{"result":{}}},{"id":"unrelated","kind":"execute","executor":"agent/a","inputs":{"__after_writer":{"ref":{"node":"writer","port":"result"}}}}],"output":{"result":{"ref":{"node":"writer","port":"result"}}}}}}`), &p); err != nil {
		t.Fatal(err)
	}
	names := runSelectionWorkflow(t, s, c, &p)
	if len(names) != 3 || names[0] != "agent/b" || names[1] != "agent/a" || names[2] != "agent/a" {
		t.Fatalf("review did not diversify from the child result's producer: %v", names)
	}
}

func runProducerSelectionCase(t *testing.T, raw string, omitRoot bool) []string {
	t.Helper()
	s := newSQLiteStoreForPreflight(t)
	c := NewCoordinator(s)
	registerWorker(t, s, "worker", []state.WorkerCapability{readyCapability("agent/a", "1")}, nil)
	warmup := minimalPlan("any")
	if names := runSelectionWorkflow(t, s, c, &warmup); len(names) != 1 || names[0] != "agent/a" {
		t.Fatalf("warmup: %v", names)
	}
	registerWorker(t, s, "worker", []state.WorkerCapability{readyCapability("agent/a", "1"), readyCapability("agent/b", "1")}, nil)
	var p plan.Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if omitRoot {
		p.Root = ""
	}
	return runSelectionWorkflow(t, s, c, &p)
}

func TestAnyAgentDiversityUsesDefaultRoot(t *testing.T) {
	names := runProducerSelectionCase(t, `{"irVersion":"1.0","name":"optional-root","root":"root","subgraphs":{"root":{"nodes":[{"id":"generate","kind":"child","childRef":"generation","maxDepth":1},{"id":"review","kind":"execute","executor":"any","inputs":{"text":{"ref":{"node":"generate","port":"result"}}}}]},"generation":{"nodes":[{"id":"writer","kind":"execute","executor":"agent/b","outputs":{"result":{}}}],"output":{"result":{"ref":{"node":"writer","port":"result"}}}}}}`, true)
	if len(names) != 2 || names[1] != "agent/a" {
		t.Fatalf("omitting optional root lost diversity: %v", names)
	}
}

func TestAnyAgentDiversityUsesOnlyConsumedChildField(t *testing.T) {
	names := runProducerSelectionCase(t, `{"irVersion":"1.0","name":"child-field","root":"root","subgraphs":{"root":{"nodes":[{"id":"generate","kind":"child","childRef":"generation","maxDepth":1},{"id":"review","kind":"execute","executor":"any","inputs":{"text":{"expr":{"op":"field","name":"text","expr":{"op":"ref","node":"generate","port":"result"}}}}}]},"generation":{"nodes":[{"id":"writer","kind":"execute","executor":"agent/b","outputs":{"result":{}}},{"id":"checker","kind":"execute","executor":"agent/a","inputs":{"__after_writer":{"ref":{"node":"writer","port":"result"}}},"outputs":{"result":{}}}],"output":{"text":{"ref":{"node":"writer","port":"result"}},"check":{"ref":{"node":"checker","port":"result"}}}}}}`, false)
	if len(names) != 3 || names[2] != "agent/a" {
		t.Fatalf("unused child field polluted producer preference: %v", names)
	}
}

func TestAnyAgentDiversityFollowsLoopCarriedValue(t *testing.T) {
	names := runProducerSelectionCase(t, `{"irVersion":"1.0","name":"loop-state","root":"root","subgraphs":{"root":{"nodes":[{"id":"writer","kind":"execute","executor":"agent/b","outputs":{"result":{}}},{"id":"loop","kind":"loop","bodyRef":"body","maxIterations":3,"initState":{"op":"object","fields":{"text":{"op":"ref","node":"writer","port":"result"},"count":{"op":"literal","value":0}}},"while":{"op":"lt","left":{"op":"field","name":"count","expr":{"op":"state"}},"right":{"op":"literal","value":3}},"stateRef":"result"},{"id":"review","kind":"execute","executor":"any","inputs":{"text":{"expr":{"op":"field","name":"text","expr":{"op":"ref","node":"loop","port":"result"}}}}}]},"body":{"nodes":[{"id":"advance","kind":"project","inputs":{"result":{"expr":{"op":"object","fields":{"text":{"op":"field","name":"text","expr":{"op":"state"}},"count":{"op":"add","left":{"op":"field","name":"count","expr":{"op":"state"}},"right":{"op":"literal","value":1}}}}}}}],"output":{"result":{"ref":{"node":"advance","port":"result"}}}}}}`, false)
	if len(names) != 2 || names[1] != "agent/a" {
		t.Fatalf("loop state lost its original producer: %v", names)
	}
}
