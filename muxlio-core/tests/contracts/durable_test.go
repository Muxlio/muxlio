package contracts_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type executor struct {
	calls []workers.ExecuteRequest
	fail  bool
}

func (x *executor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	x.calls = append(x.calls, req)
	if x.fail {
		req.Inputs["value"] = "executor mutation"
		return workers.ExecuteResult{}, errors.New("temporary failure")
	}
	return workers.ExecuteResult{Outputs: map[string]any{"value": req.Inputs["value"]}}, nil
}

func workflow() *plan.Plan {
	return &plan.Plan{IRVersion: "1.0", Name: "contract", Root: "root", Subgraphs: map[string]plan.Subgraph{
		"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/echo", Task: "original",
			Inputs:  map[string]plan.Binding{"value": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`"immutable"`)}}},
			Outputs: map[string]plan.OutputDecl{"value": {}}}}},
	}}
}

func TestDurableContracts(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store
			var reopen func()
			if backend == "memory" {
				store = state.New()
				reopen = func() {}
			} else {
				path := filepath.Join(t.TempDir(), "state.db")
				open := func() state.Store {
					s, err := sqlite.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					return s
				}
				store = open()
				reopen = func() {
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					store = open()
				}
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}()
			x := &executor{fail: true}
			p := workflow()
			engine := plan.NewEngineWithStore(p, x, store)
			id, err := engine.CreateRun(json.RawMessage(`{"seed":1}`))
			if err != nil {
				t.Fatal(err)
			}
			original, _ := store.GetRun(id)
			if err := store.SaveWorkflowVersion(original.PlanDigest, original.PlanJSON); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveWorkflowVersion(original.PlanDigest, original.PlanJSON); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveWorkflowVersion(original.PlanDigest, []byte(`{}`)); err == nil {
				t.Fatal("workflow version overwritten")
			}
			if err := store.UpdateRunPlan(id, original.PlanJSON, original.PlanDigest); err != nil {
				t.Fatal(err)
			}
			if err := engine.Run(id); err == nil {
				t.Fatal("expected first attempt to fail")
			}
			acts, err := store.ListActivations(id)
			if err != nil || len(acts) != 1 || len(acts[0].Inputs) == 0 {
				t.Fatalf("activations=%+v err=%v", acts, err)
			}
			activation := acts[0]
			reopen()
			// A new definition and a new engine must not change this run's task or inputs.
			changed := workflow()
			changed.Subgraphs["root"].Nodes[0].Task = "changed"
			changedEngine := plan.NewEngineWithStore(changed, x, store)
			if err := changedEngine.RepinRunPlan(id, changed); err == nil {
				t.Fatal("plan replacement accepted")
			}
			if err := store.UpdateRunPlan(id, json.RawMessage(`{}`), "other"); err == nil {
				t.Fatal("store changed pinned plan")
			}
			x.fail = false
			if err := changedEngine.Retry(id); err != nil {
				t.Fatal(err)
			}
			if err := changedEngine.Run(id); err != nil {
				t.Fatal(err)
			}
			if len(x.calls) != 2 || x.calls[1].Task != "original" || x.calls[1].Inputs["value"] != "immutable" {
				t.Fatalf("requests=%+v", x.calls)
			}
			attempts, err := store.ListExecutionAttempts(id, activation.ID)
			if err != nil || len(attempts) != 2 {
				t.Fatalf("attempts=%+v err=%v", attempts, err)
			}
			if attempts[0].ActivationID != attempts[1].ActivationID || attempts[0].ID == attempts[1].ID || attempts[1].RetryNumber != 2 {
				t.Fatalf("retry changed logical identity: %+v", attempts)
			}
			acts, _ = store.ListActivations(id)
			if len(acts) != 1 || acts[0].ID != activation.ID || string(acts[0].Inputs) != string(activation.Inputs) {
				t.Fatalf("activation changed: %+v", acts)
			}
			run, _ := store.GetRun(id)
			if run.PlanDigest != original.PlanDigest || string(run.PlanJSON) != string(original.PlanJSON) {
				t.Fatal("run pin changed")
			}
			events, err := store.GetEvents(id, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for i, event := range events {
				if event.Seq != i+1 {
					t.Fatalf("non-contiguous transition sequence: %+v", events)
				}
			}
			reopen()
			// A restarted worker/engine does not cause terminal work to execute again.
			restarted := plan.NewEngineWithStore(changed, x, store)
			if err := restarted.Run(id); err == nil {
				t.Fatal("terminal run accepted")
			}
			if len(x.calls) != 2 {
				t.Fatal("completed activation executed after restart")
			}
		})
	}
}

func TestMemoryRunInputAndPinCannotBeMutatedThroughAliases(t *testing.T) {
	store := state.New()
	input := map[string]any{"nested": map[string]any{"value": "original"}}
	run := &state.Run{ID: "r", Input: input, PlanJSON: json.RawMessage(`{"name":"original"}`), PlanDigest: "original"}
	if err := store.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	input["nested"].(map[string]any)["value"] = "changed"
	run.PlanJSON[0] = '['
	for _, list := range []bool{false, true} {
		var snapshot *state.Run
		if list {
			runs, _ := store.ListRuns()
			snapshot = runs[0]
		} else {
			snapshot, _ = store.GetRun("r")
		}
		snapshot.Input.(map[string]any)["nested"].(map[string]any)["value"] = "changed"
		snapshot.PlanDigest = "changed"
		snapshot.PlanJSON[0] = '['
	}
	saved, _ := store.GetRun("r")
	if saved.Input.(map[string]any)["nested"].(map[string]any)["value"] != "original" || saved.PlanDigest != "original" || saved.PlanJSON[0] != '{' {
		t.Fatalf("mutable run: %+v", saved)
	}
}

func TestTimerDeadlineSurvivesReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := workflow()
	seconds := 3600.0
	p.Subgraphs["root"] = plan.Subgraph{Nodes: []plan.Node{{ID: "timer", Kind: plan.KindWait, WaitFor: "timer", TimerSeconds: &seconds}}}
	engine := plan.NewEngineWithStore(p, &executor{}, store)
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Second)
	if err := store.ScheduleTimer(id+":timer:timer", id, id+":timer", due, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DueTimers(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine = plan.NewEngineWithStore(p, &executor{}, store)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	engine.Context = ctx
	// The persisted deadline has elapsed. Replaying must not wait another hour.
	if err := engine.Run(id); err != nil {
		t.Fatal(err)
	}
	timer, err := store.GetTimer(id + ":timer:timer")
	if err != nil || !timer.DueAt.Equal(due) || timer.State != "fired" {
		t.Fatalf("timer=%+v err=%v", timer, err)
	}
}

type mutateOnceExecutor struct{ executor }

func (x *mutateOnceExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	x.fail = len(x.calls) == 0
	return x.executor.Execute(req)
}

func TestAutomaticRetryPreservesInputsAndLogicalActivation(t *testing.T) {
	p := workflow()
	p.Subgraphs["root"].Nodes[0].Retry = &plan.RetryPolicy{MaxAttempts: 2, Effect: "retryable"}
	x := &mutateOnceExecutor{}
	engine := plan.NewEngine(p, x)
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(id); err != nil {
		t.Fatal(err)
	}
	if len(x.calls) != 2 || x.calls[1].Inputs["value"] != "immutable" || x.calls[0].ActivationID != x.calls[1].ActivationID || x.calls[0].AttemptID == x.calls[1].AttemptID {
		t.Fatalf("retry failed to preserve immutable logical occurrence: %+v", x.calls)
	}
}

func TestActivationCheckpointIsolation(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store = state.New()
			if backend == "sqlite" {
				s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				store = s
			}
			defer store.Close()
			if err := store.CreateRun(&state.Run{ID: "r"}); err != nil {
				t.Fatal(err)
			}
			outputs := map[string]any{"nested": map[string]any{"value": "original"}}
			a := &state.Activation{ID: "a", RunID: "r", NodeID: "task", State: "queued", Inputs: json.RawMessage(`{"value":"original"}`)}
			if err := store.CreateActivation(a); err != nil {
				t.Fatal(err)
			}
			a.Inputs[0] = '['
			if err := store.SetActivationOutputs("a", outputs); err != nil {
				t.Fatal(err)
			}
			outputs["nested"].(map[string]any)["value"] = "changed"
			got, err := store.GetActivation("a")
			if err != nil {
				t.Fatal(err)
			}
			if got.Outputs["nested"].(map[string]any)["value"] != "original" {
				t.Fatal("caller changed a succeeded checkpoint")
			}
			got.Outputs["nested"].(map[string]any)["value"] = "changed"
			got.Inputs[0] = '['
			acts, err := store.ListActivations("r")
			if err != nil {
				t.Fatal(err)
			}
			acts[0].Outputs["nested"].(map[string]any)["value"] = "changed"
			acts[0].Inputs[0] = '['
			saved, err := store.GetActivation("a")
			if err != nil {
				t.Fatal(err)
			}
			if saved.Outputs["nested"].(map[string]any)["value"] != "original" || string(saved.Inputs) != `{"value":"original"}` {
				t.Fatalf("checkpoint changed through read aliases: %+v", saved)
			}
		})
	}
}

func TestTimerSchedulingPreservesDeadlineAndState(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store = state.New()
			if backend == "sqlite" {
				s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				store = s
			}
			defer store.Close()
			if err := store.CreateRun(&state.Run{ID: "r"}); err != nil {
				t.Fatal(err)
			}
			due := time.Now().Add(-time.Hour)
			for _, status := range []string{"pending", "fired", "cancelled"} {
				if err := store.ScheduleTimer(status, "r", "a", due, nil); err != nil {
					t.Fatal(err)
				}
				switch status {
				case "fired":
					if _, err := store.DueTimers(time.Now()); err != nil {
						t.Fatal(err)
					}
				case "cancelled":
					if err := store.CancelTimer(status); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.ScheduleTimer(status, "r", "a", time.Now().Add(time.Hour), nil); err != nil {
					t.Fatal(err)
				}
				timer, err := store.GetTimer(status)
				if err != nil {
					t.Fatal(err)
				}
				if !timer.DueAt.Equal(due) || timer.State != status {
					t.Fatalf("replay changed timer: %+v", timer)
				}
			}
		})
	}
}
