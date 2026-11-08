package recovery_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type reconciliationCrash struct{}

// Crash only after the underlying store has committed the selected write.
type reconciliationStore struct {
	state.Store
	stage string
}

func (s *reconciliationStore) crash(stage string) {
	if s.stage == stage {
		panic(reconciliationCrash{})
	}
}

func (s *reconciliationStore) SetActivationOutputs(id string, outputs map[string]any) error {
	if err := s.Store.SetActivationOutputs(id, outputs); err != nil {
		return err
	}
	if outputs["__muxlioLoopProgress"] != nil {
		s.crash("checkpoint")
	}
	act, err := s.GetActivation(id)
	if err != nil {
		return err
	}
	if act.NodeID == "first" {
		s.crash("first-child")
	}
	if act.NodeID == "work" {
		s.crash("child-work")
	}
	return nil
}

func (s *reconciliationStore) CheckpointActivation(id string, outputs map[string]any) error {
	// The narrow interface keeps this regression compilable against the old Store.
	checkpoint := s.Store.(interface {
		CheckpointActivation(string, map[string]any) error
	})
	if err := checkpoint.CheckpointActivation(id, outputs); err != nil {
		return err
	}
	s.crash("checkpoint")
	return nil
}

func (s *reconciliationStore) TransitionRunState(id string, expected []string, next string) (bool, error) {
	changed, err := s.Store.TransitionRunState(id, expected, next)
	if err == nil && changed && next == plan.RunPaused {
		s.crash("paused")
	}
	return changed, err
}

func (s *reconciliationStore) AppendEvent(runID, kind, subject string, payload map[string]any) error {
	if err := s.Store.AppendEvent(runID, kind, subject, payload); err != nil {
		return err
	}
	if kind == state.EventLoopIterated {
		s.crash("before-checkpoint")
	}
	return nil
}

func runUntilReconciliationCrash(t *testing.T, engine *plan.Engine, id string) {
	t.Helper()
	defer func() {
		if caught := recover(); caught != (reconciliationCrash{}) {
			t.Fatalf("expected injected crash, got %v", caught)
		}
	}()
	if err := engine.Run(id); err != nil {
		t.Fatal(err)
	}
}

func reconciliationBackend(t *testing.T, backend string) (state.Store, func() state.Store) {
	t.Helper()
	if backend == "memory" {
		s := state.New()
		t.Cleanup(func() { s.Close() })
		return s, func() state.Store { return s }
	}
	path := filepath.Join(t.TempDir(), "reconciliation.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, func() state.Store {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = sqlite.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func createReconciliationRun(t *testing.T, p *plan.Plan, s state.Store, executor workers.Executor) (*plan.Engine, string) {
	t.Helper()
	for _, diagnostic := range plan.Validate(p) {
		if diagnostic.Severity == "error" {
			t.Fatalf("invalid test plan: %+v", diagnostic)
		}
	}
	engine := plan.NewEngineWithStore(p, executor, s)
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return engine, id
}

func resumeReconciliationRun(t *testing.T, s state.Store, executor workers.Executor, id string) {
	t.Helper()
	engines, err := plan.RecoverRuns(s, executor)
	if err != nil {
		t.Fatal(err)
	}
	if engines[id] == nil {
		t.Fatal("run was not recovered")
	}
	if err := engines[id].Run(id); err != nil {
		t.Fatal(err)
	}
}

func exhaustedReconciliationPlan(onExhausted string) *plan.Plan {
	max := 1
	return &plan.Plan{IRVersion: "1.0", Name: "exhausted-restart", Root: "root", Subgraphs: map[string]plan.Subgraph{
		"root": {Nodes: []plan.Node{
			{ID: "loop", Kind: plan.KindLoop, BodyRef: "body", MaxIterations: &max, OnExhausted: onExhausted, While: &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)}, InitState: &plan.Expr{Op: "literal", Value: json.RawMessage(`0`)}},
			{ID: "downstream", Kind: plan.KindExecute, Executor: "test/echo", Inputs: map[string]plan.Binding{"value": {Ref: &plan.ValueRef{Node: "loop", Port: "result"}}}},
		}},
		"body": {Nodes: []plan.Node{{ID: "step", Kind: plan.KindProject, Inputs: map[string]plan.Binding{"value": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`1`)}}}}}, Output: map[string]plan.Binding{"value": {Ref: &plan.ValueRef{Node: "step", Port: "value"}}}},
	}}
}

func TestExhaustedLoopCheckpointRestart(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, onExhausted := range []string{"pause", ""} {
			for _, stage := range []string{"before-checkpoint", "checkpoint", "paused"} {
				t.Run(backend+"/"+onExhausted+"/"+stage, func(t *testing.T) {
					s, reopen := reconciliationBackend(t, backend)
					executor := workers.NewFake()
					engine, id := createReconciliationRun(t, exhaustedReconciliationPlan(onExhausted), &reconciliationStore{Store: s, stage: stage}, executor)
					runUntilReconciliationCrash(t, engine, id)
					if stage == "checkpoint" {
						act, err := s.GetActivation(id + ":loop")
						if err != nil || act.State != plan.ActWaiting {
							t.Fatalf("checkpoint exposed completion: activation=%+v err=%v", act, err)
						}
					}
					s = reopen()
					resumeReconciliationRun(t, s, executor, id)
					run, err := s.GetRun(id)
					if err != nil || run.State != plan.RunPaused || len(executor.Calls()) != 0 {
						t.Fatalf("run=%+v err=%v downstream calls=%d", run, err, len(executor.Calls()))
					}
					act, err := s.GetActivation(id + ":loop")
					if err != nil || act.State != plan.ActWaiting || act.Outputs["__muxlioLoopProgress"] == nil || act.Outputs["result"] != float64(1) {
						t.Fatalf("lost loop progress: activation=%+v err=%v", act, err)
					}
				})
			}
		}
	}
}

func TestNullLoopCheckpointRestart(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, policy := range []struct{ name, value string }{{"explicit-pause", "pause"}, {"default-pause", ""}} {
			for _, checkpointState := range []string{plan.ActWaiting, plan.ActSucceeded} {
				t.Run(backend+"/"+policy.name+"/"+checkpointState, func(t *testing.T) {
					s, reopen := reconciliationBackend(t, backend)
					p := exhaustedReconciliationPlan(policy.value)
					p.Subgraphs["body"].Nodes[0].Inputs["value"].Expr.Value = json.RawMessage(`null`)
					executor := workers.NewFake()
					engine, id := createReconciliationRun(t, p, &reconciliationStore{Store: s, stage: "checkpoint"}, executor)
					runUntilReconciliationCrash(t, engine, id)
					before, err := s.GetActivation(id + ":loop")
					if err != nil || before.State != plan.ActWaiting {
						t.Fatalf("checkpoint exposed completion: activation=%+v err=%v", before, err)
					}
					if result, present := before.Outputs["result"]; !present || result != nil || before.Outputs["__muxlioLoopProgress"] == nil {
						t.Fatalf("expected durable null checkpoint, got %+v", before.Outputs)
					}
					if checkpointState == plan.ActSucceeded {
						// Older coordinators committed these outputs as succeeded
						// before they could mark the exhausted loop waiting.
						if err := s.SetActivationState(id+":loop", plan.ActSucceeded); err != nil {
							t.Fatal(err)
						}
					}
					s = reopen()
					resumeReconciliationRun(t, s, executor, id)
					run, err := s.GetRun(id)
					if err != nil || run.State != plan.RunPaused || len(executor.Calls()) != 0 {
						t.Fatalf("run=%+v err=%v downstream calls=%d", run, err, len(executor.Calls()))
					}
					after, err := s.GetActivation(id + ":loop")
					if err != nil || after.State != plan.ActWaiting || !reflect.DeepEqual(before.Outputs, after.Outputs) {
						t.Fatalf("checkpoint changed: before=%+v after=%+v err=%v", before, after, err)
					}
				})
			}
		}
	}
}

func TestLegacySucceededLoopCheckpointRestart(t *testing.T) {
	s, reopen := reconciliationBackend(t, "sqlite")
	executor := workers.NewFake()
	_, id := createReconciliationRun(t, exhaustedReconciliationPlan("pause"), s, executor)
	if err := s.SetRunState(id, plan.RunRunning); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: id + ":loop", RunID: id, NodeID: "loop", State: plan.ActRunning}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivationOutputs(id+":loop", map[string]any{"result": 1, "__muxlioLoopProgress": map[string]any{"iteration": 1, "exhausted": true}}); err != nil {
		t.Fatal(err)
	}
	s = reopen()
	resumeReconciliationRun(t, s, executor, id)
	run, _ := s.GetRun(id)
	if run.State != plan.RunPaused || len(executor.Calls()) != 0 {
		t.Fatalf("legacy checkpoint bypassed pause: state=%s calls=%d", run.State, len(executor.Calls()))
	}
}

func TestChildInvocationScopes(t *testing.T) {
	for _, stage := range []string{"", "first-child", "child-work"} {
		t.Run(map[string]string{"": "uninterrupted", "first-child": "between-invocations", "child-work": "inside-invocation"}[stage], func(t *testing.T) {
			s, reopen := reconciliationBackend(t, "sqlite")
			depth := 1
			p := &plan.Plan{IRVersion: "1.0", Name: "child-invocations", Root: "root", Subgraphs: map[string]plan.Subgraph{
				"root": {Nodes: []plan.Node{
					{ID: "first", Kind: plan.KindChild, ChildRef: "body", MaxDepth: &depth},
					{ID: "second", Kind: plan.KindChild, ChildRef: "body", MaxDepth: &depth, Inputs: map[string]plan.Binding{"previous": {Ref: &plan.ValueRef{Node: "first", Port: "result"}}}},
				}},
				"body": {Nodes: []plan.Node{{ID: "work", Kind: plan.KindExecute, Executor: "test/echo", Outputs: map[string]plan.OutputDecl{"value": {}}}}, Output: map[string]plan.Binding{"value": {Ref: &plan.ValueRef{Node: "work", Port: "value"}}}},
			}}
			executor := workers.NewFake().ByNode("work", workers.ExecuteResult{Outputs: map[string]any{"value": "ok"}})
			engine, id := createReconciliationRun(t, p, &reconciliationStore{Store: s, stage: stage}, executor)
			if stage != "" {
				runUntilReconciliationCrash(t, engine, id)
				s = reopen()
				resumeReconciliationRun(t, s, executor, id)
			} else if err := engine.Run(id); err != nil {
				t.Fatal(err)
			}
			run, _ := s.GetRun(id)
			acts, err := s.ListActivations(id)
			if err != nil {
				t.Fatal(err)
			}
			workIDs := map[string]bool{}
			for _, act := range acts {
				if act.NodeID == "work" && act.State == plan.ActSucceeded {
					workIDs[act.ID] = true
				}
			}
			if run.State != plan.RunSucceeded || len(executor.Calls()) != 2 || len(workIDs) != 2 {
				t.Fatalf("state=%s calls=%d work activations=%v", run.State, len(executor.Calls()), workIDs)
			}
		})
	}
}

// Delimiters in both caller and body IDs must not merge distinct effects.
func TestChildDelimitedInvocationScopes(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, restart := range []bool{false, true} {
			name := "uninterrupted"
			if restart {
				name = "restart-between-invocations"
			}
			t.Run(backend+"/"+name, func(t *testing.T) {
				s, reopen := reconciliationBackend(t, backend)
				depth := 1
				p := &plan.Plan{IRVersion: "1.0", Name: "delimited-child", Root: "root", Subgraphs: map[string]plan.Subgraph{
					"root": {Nodes: []plan.Node{
						{ID: "first", Kind: plan.KindChild, ChildRef: "body", MaxDepth: &depth},
						{ID: "first:work", Kind: plan.KindChild, ChildRef: "body", MaxDepth: &depth, Inputs: map[string]plan.Binding{"previous": {Ref: &plan.ValueRef{Node: "first", Port: "result"}}}},
					}},
					"body": {Nodes: []plan.Node{
						{ID: "work:step", Kind: plan.KindExecute, Executor: "test/echo"},
						{ID: "step", Kind: plan.KindExecute, Executor: "test/echo"},
					}},
				}}
				executor := workers.NewFake()
				stage := ""
				if restart {
					stage = "first-child"
				}
				engine, id := createReconciliationRun(t, p, &reconciliationStore{Store: s, stage: stage}, executor)
				if restart {
					runUntilReconciliationCrash(t, engine, id)
					if len(executor.Calls()) != 2 {
						t.Fatalf("calls before restart = %d", len(executor.Calls()))
					}
					s = reopen()
					resumeReconciliationRun(t, s, executor, id)
				} else if err := engine.Run(id); err != nil {
					t.Fatal(err)
				}
				run, err := s.GetRun(id)
				if err != nil {
					t.Fatal(err)
				}
				acts, err := s.ListActivations(id)
				if err != nil {
					t.Fatal(err)
				}
				work := map[string]bool{}
				for _, act := range acts {
					if len(act.ScopePath) > 0 && act.State == plan.ActSucceeded {
						work[act.ID] = true
					}
				}
				if run.State != plan.RunSucceeded || len(executor.Calls()) != 4 || len(work) != 4 {
					t.Fatalf("state=%s calls=%d nested histories=%v", run.State, len(executor.Calls()), work)
				}
			})
		}
	}
}

// Map owner IDs and keys are separate identity fields, including when the
// map body invokes a child. Escaping only the final scope string is insufficient.
func TestChildInvocationUnderDelimitedMapScopes(t *testing.T) {
	for _, concurrency := range []int{1, 2} {
		t.Run(map[int]string{1: "serial", 2: "parallel"}[concurrency], func(t *testing.T) {
			s, _ := reconciliationBackend(t, "memory")
			depth, maxItems := 1, 1
			p := &plan.Plan{IRVersion: "1.0", Name: "child-map-identities", Root: "root", Subgraphs: map[string]plan.Subgraph{
				"root": {Nodes: []plan.Node{
					{ID: "outer", Kind: plan.KindMap, BodyRef: "item", MaxItems: &maxItems, Concurrency: &concurrency, Items: &plan.Expr{Op: "literal", Value: json.RawMessage(`["a:b"]`)}, Key: &plan.Expr{Op: "item"}},
					{ID: "outer:a", Kind: plan.KindMap, BodyRef: "item", MaxItems: &maxItems, Concurrency: &concurrency, Items: &plan.Expr{Op: "literal", Value: json.RawMessage(`["b"]`)}, Key: &plan.Expr{Op: "item"}, Inputs: map[string]plan.Binding{"previous": {Ref: &plan.ValueRef{Node: "outer", Port: "items"}}}},
				}},
				"item": {Nodes: []plan.Node{{ID: "call", Kind: plan.KindChild, ChildRef: "body", MaxDepth: &depth}}},
				"body": {Nodes: []plan.Node{{ID: "work", Kind: plan.KindExecute, Executor: "test/echo"}}},
			}}
			executor := workers.NewFake()
			engine, id := createReconciliationRun(t, p, s, executor)
			if err := engine.Run(id); err != nil {
				t.Fatal(err)
			}
			acts, err := s.ListActivations(id)
			if err != nil {
				t.Fatal(err)
			}
			work := map[string]bool{}
			for _, act := range acts {
				if act.NodeID == "work" && act.State == plan.ActSucceeded {
					work[act.ID] = true
				}
			}
			if len(executor.Calls()) != 2 || len(work) != 2 {
				t.Fatalf("calls=%d work histories=%v", len(executor.Calls()), work)
			}
		})
	}
}

// A structural/executable local-ID collision is valid, but does not strand
// the structural activation even though recovery classifies it conservatively.
func TestRecoveryLocalIDCollisionDoesNotStrand(t *testing.T) {
	s, reopen := reconciliationBackend(t, "sqlite")
	p := &plan.Plan{IRVersion: "1.0", Name: "local-ids", Root: "root", Subgraphs: map[string]plan.Subgraph{
		"root": {Nodes: []plan.Node{{ID: "choose", Kind: plan.KindBranch, Condition: &plan.Expr{Op: "literal", Value: json.RawMessage(`true`)}, ThenRef: "yes", ElseRef: "no"}}, Output: map[string]plan.Binding{"value": {Ref: &plan.ValueRef{Node: "choose", Port: "result"}}}},
		"yes":  {Nodes: []plan.Node{{ID: "same", Kind: plan.KindProject, Inputs: map[string]plan.Binding{"value": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`"ok"`)}}}}}, Output: map[string]plan.Binding{"value": {Ref: &plan.ValueRef{Node: "same", Port: "value"}}}},
		"no":   {Nodes: []plan.Node{{ID: "same", Kind: plan.KindExecute, Executor: "test/echo"}}},
	}}
	executor := workers.NewFake()
	_, id := createReconciliationRun(t, p, s, executor)
	if err := s.SetRunState(id, plan.RunRunning); err != nil {
		t.Fatal(err)
	}
	actID := id + ":branch%3Achoose%3Atrue:same"
	if err := s.CreateActivation(&state.Activation{ID: actID, RunID: id, NodeID: "same", ScopePath: []string{"branch:choose:true"}, State: plan.ActRunning}); err != nil {
		t.Fatal(err)
	}
	s = reopen()
	resumeReconciliationRun(t, s, executor, id)
	run, err := s.GetRun(id)
	if err != nil || run.State != plan.RunSucceeded || run.Output != "ok" || len(executor.Calls()) != 0 {
		t.Fatalf("run=%+v err=%v calls=%d", run, err, len(executor.Calls()))
	}
}

func TestLegacyChildHistoryRequiresReconciliation(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, owned := range []bool{false, true} {
			for _, scenario := range []string{"recovery-blocked", "failed", "cancelled", "direct-running", "direct-paused"} {
				history := "unowned/"
				if owned {
					history = "delimiter-joined/"
				}
				t.Run(backend+"/"+history+scenario, func(t *testing.T) {
					s, reopen := reconciliationBackend(t, backend)
					depth := 1
					p := &plan.Plan{IRVersion: "1.0", Name: "legacy-child", Root: "root", Subgraphs: map[string]plan.Subgraph{
						"root": {Nodes: []plan.Node{{ID: "parent", Kind: plan.KindChild, ChildRef: "body", MaxDepth: &depth}}},
						"body": {Nodes: []plan.Node{{ID: "work", Kind: plan.KindExecute, Executor: "test/echo"}}},
					}}
					executor := workers.NewFake()
					_, id := createReconciliationRun(t, p, s, executor)
					runState, parentState := plan.RunRunning, plan.ActRunning
					switch scenario {
					case "failed":
						runState, parentState = plan.RunFailed, plan.ActFailed
					case "cancelled":
						runState, parentState = plan.RunCancelled, plan.ActCancelled
					case "direct-paused":
						runState = plan.RunPaused
					}
					// The old interpreter completed an effect before its parent
					// committed. Retrying must preserve this history without replay.
					childID, childScope := id+":child:body:work", []string{"child:body"}
					if owned {
						childID, childScope = id+":child:body:invoke:parent:work", []string{"child:body", "invoke:parent"}
					}
					for _, act := range []*state.Activation{
						{ID: id + ":parent", RunID: id, NodeID: "parent", State: parentState},
						{ID: childID, RunID: id, NodeID: "work", ScopePath: childScope, State: plan.ActSucceeded, Outputs: map[string]any{"value": "completed"}},
					} {
						if err := s.CreateActivation(act); err != nil {
							t.Fatal(err)
						}
					}
					if err := s.SetRunState(id, runState); err != nil {
						t.Fatal(err)
					}
					s = reopen()
					if scenario == "recovery-blocked" {
						engines, err := plan.RecoverRuns(s, executor)
						if err != nil {
							t.Fatal(err)
						}
						run, err := s.GetRun(id)
						if err != nil || engines[id] != nil || run.State != plan.RunFailed || !strings.Contains(run.Error, "manual reconciliation") {
							t.Fatalf("ambiguous history recovered: run=%+v engines=%v err=%v", run, engines, err)
						}
					}
					beforeRun, err := s.GetRun(id)
					if err != nil {
						t.Fatal(err)
					}
					beforeActs, err := s.ListActivations(id)
					if err != nil {
						t.Fatal(err)
					}
					// Match the public retry control: a fresh engine calls Retry
					// without RecoverRuns. Terminal histories have no recovery error.
					engine := plan.NewEngineWithStore(p, executor, s)
					if !strings.HasPrefix(scenario, "direct-") {
						err := engine.Retry(id)
						if err == nil || !strings.Contains(err.Error(), "manual reconciliation") {
							t.Errorf("retry should require reconciliation, got %v", err)
						}
					}
					// Even callers that bypass Retry cannot execute legacy work.
					if err := engine.Run(id); err == nil || (strings.HasPrefix(scenario, "direct-") && !strings.Contains(err.Error(), "manual reconciliation")) {
						t.Errorf("run should reject unresolved history, got %v", err)
					}
					afterRun, err := s.GetRun(id)
					if err != nil {
						t.Fatal(err)
					}
					afterActs, err := s.ListActivations(id)
					if err != nil {
						t.Fatal(err)
					}
					// ListActivations has no ordering guarantee on the memory store.
					sort.Slice(beforeActs, func(i, j int) bool { return beforeActs[i].ID < beforeActs[j].ID })
					sort.Slice(afterActs, func(i, j int) bool { return afterActs[i].ID < afterActs[j].ID })
					if len(executor.Calls()) != 0 || !reflect.DeepEqual(beforeRun, afterRun) || !reflect.DeepEqual(beforeActs, afterActs) {
						t.Fatalf("rejected history changed: calls=%d run=%+v activations=%+v", len(executor.Calls()), afterRun, afterActs)
					}
					if _, err := s.GetActivation(id + ":child%3Abody:invoke%3Aparent:work"); err == nil {
						t.Fatal("created replacement work identity for unresolved history")
					}
				})
			}
		}
	}
}
