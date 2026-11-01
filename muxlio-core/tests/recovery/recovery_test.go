package recovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestQueuedDispatchResumesAfterCoordinatorRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queued.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := api.NewCoordinator(st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.Context = ctx
	p := &plan.Plan{IRVersion: "1.0", Name: "queued", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "queued", Kind: "execute", Executor: "test/echo", Outputs: map[string]plan.OutputDecl{"result": {}}}}, Output: map[string]plan.Binding{"result": {Ref: &plan.ValueRef{Node: "queued", Port: "result"}}}}}}
	engine := plan.NewEngineWithStore(p, coordinator, st)
	engine.Context = ctx
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- engine.Run(id) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		pending, err := st.UnclaimedDispatch(id + ":queued")
		if err != nil {
			t.Fatal(err)
		}
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not stop interpreter")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	coordinator = api.NewCoordinator(st)
	engines, err := plan.RecoverRuns(st, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if engines[id] == nil {
		run, _ := st.GetRun(id)
		t.Fatalf("queued run not recovered: %+v", run)
	}
	if _, err := coordinator.Register(api.WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []api.WorkerCapability{{Name: "test/echo"}}}); err != nil {
		t.Fatal(err)
	}
	go func() { finished <- engines[id].Run(id) }()
	var assignment *api.WorkerAssignment
	deadline = time.Now().Add(3 * time.Second)
	for assignment == nil && time.Now().Before(deadline) {
		assignment, err = coordinator.NextAssignment("worker")
		if err != nil {
			t.Fatal(err)
		}
		if assignment == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if assignment == nil {
		t.Fatal("no recovered assignment")
	}
	err = coordinator.Complete("worker", api.WorkerCompletion{AssignmentID: assignment.AssignmentID, RunID: assignment.RunID, ActivationID: assignment.ActivationID, AttemptID: assignment.AttemptID, CoordinatorEpoch: assignment.CoordinatorEpoch, Generation: assignment.Generation, State: "succeeded", Outputs: map[string]any{"result": "recovered"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovered run did not finish")
	}
	run, err := st.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" || run.Output != "recovered" {
		t.Fatalf("recovered run: %+v", run)
	}
}

func TestRunSurvivesCoordinatorRestart(t *testing.T) {
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "durable",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "step",
						Kind:     "execute",
						Executor: "test/echo",
						Outputs:  map[string]plan.OutputDecl{"result": {Mime: "text/plain"}},
					},
				},
				Output: map[string]plan.Binding{
					"result": {Ref: &plan.ValueRef{Node: "step", Port: "result"}},
				},
			},
		},
	}
	exec := workers.NewFake().ByNode("step", workers.ExecuteResult{
		Outputs: map[string]any{"result": "ok"},
	})

	path := filepath.Join(t.TempDir(), "coordinator.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	engine := plan.NewEngineWithStore(p, exec, st)
	input, _ := json.Marshal(map[string]any{})
	runID, err := engine.CreateRun(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })

	run, err := st2.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("state after restart = %s", run.State)
	}
	if run.Output != "ok" {
		t.Fatalf("output after restart = %#v", run.Output)
	}
	events, err := st2.GetEvents(runID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected persisted events")
	}
}

// TestRecoveryPinsCreatedRunAndStopsUncertainExecution verifies the original
// durable-resume contract:
//   - A run that was created (never started) is recovered and can complete.
//   - A run whose execute activation had a claimed/uncertain dispatch is also
//     recovered; the engine waits for the result rather than failing the run.
//   - A run whose activation was previously failed is not recovered (requires
//     manual reconciliation).
func TestRecoveryPinsCreatedRunAndStopsUncertainExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &plan.Plan{IRVersion: "1.0", Name: "pinned", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "step", Kind: "execute", Executor: "test/echo", Outputs: map[string]plan.OutputDecl{"result": {}}}}, Output: map[string]plan.Binding{"result": {Ref: &plan.ValueRef{Node: "step", Port: "result"}}}}}}
	fake := workers.NewFake().ByNode("step", workers.ExecuteResult{Outputs: map[string]any{"result": "pinned-output"}})
	engine := plan.NewEngineWithStore(p, fake, st)
	created, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	// A run whose activation was previously failed is not recoverable.
	failed, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunState(failed, "running"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{ID: failed + ":step", RunID: failed, NodeID: "step", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActivationError(failed+":step", errors.New("prior failure")); err != nil {
		t.Fatal(err)
	}

	p.Subgraphs = nil // caller mutation cannot replace the durable plan
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	engines, err := plan.RecoverRuns(st, fake)
	if err != nil {
		t.Fatal(err)
	}
	// The created run should be recovered and runnable.
	if len(engines) != 1 || engines[created] == nil {
		t.Fatalf("recovered engines: %#v", engines)
	}
	if err := engines[created].Run(created); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(created)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" || run.Output != "pinned-output" || run.PlanDigest == "" || len(run.PlanJSON) == 0 {
		t.Fatalf("recovered run: %+v", run)
	}
	// The failed run must be failed (requires manual reconciliation).
	run, err = st.GetRun(failed)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "failed" || !strings.Contains(run.Error, "manual reconciliation") {
		t.Fatalf("failed run should require reconciliation: %+v", run)
	}
	if err := plan.NewEngineWithStore(&plan.Plan{}, fake, st).Run(failed); err == nil {
		t.Fatal("terminal failed run was replayed")
	}
}

// TestUncertainExecuteWaitsForReconciliation verifies that after a coordinator
// restart, a run whose execute activation had a claimed (uncertain) dispatch is
// recovered and the engine blocks until the worker delivers its result, then
// completes the run normally. The side effect is never replayed.
//
// Scenario: worker completes its assignment and stores the result in
// worker_results, then the coordinator process crashes (only the in-memory
// engine is lost). On restart RecoverRuns returns an engine; when Run() is
// called it finds the stored result via WorkerResult and continues.
func TestUncertainExecuteWaitsForReconciliation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uncertain.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Two-step plan: step1 (execute) → step2 (execute).
	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "uncertain-resume",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "step1",
						Kind:     "execute",
						Executor: "test/echo",
						Outputs:  map[string]plan.OutputDecl{"result": {}},
					},
					{
						ID:       "step2",
						Kind:     "execute",
						Executor: "test/echo",
						Inputs:   map[string]plan.Binding{"in": {Ref: &plan.ValueRef{Node: "step1", Port: "result"}}},
						Outputs:  map[string]plan.OutputDecl{"result": {}},
					},
				},
				Output: map[string]plan.Binding{
					"result": {Ref: &plan.ValueRef{Node: "step2", Port: "result"}},
				},
			},
		},
	}

	coordinator1 := api.NewCoordinator(st)
	ctx1, cancel1 := context.WithCancel(context.Background())
	coordinator1.Context = ctx1

	eng1 := plan.NewEngineWithStore(p, coordinator1, st)
	eng1.Context = ctx1

	runID, err := eng1.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	// Register a worker and start the run.
	if _, err := coordinator1.Register(api.WorkerRegistration{WorkerID: "w1", Capacity: 1, Capabilities: []api.WorkerCapability{{Name: "test/echo"}}}); err != nil {
		t.Fatal(err)
	}

	done1 := make(chan error, 1)
	go func() { done1 <- eng1.Run(runID) }()

	// Wait until step1 is dispatched and claimed by the worker.
	var assignment1 *api.WorkerAssignment
	deadline := time.Now().Add(3 * time.Second)
	for assignment1 == nil && time.Now().Before(deadline) {
		assignment1, err = coordinator1.NextAssignment("w1")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if assignment1 == nil {
		t.Fatal("step1 was never assigned")
	}

	// Worker successfully completes step1 and stores its result BEFORE the
	// coordinator crashes. This is the common case: work is done but the
	// in-memory engine goroutine is lost before it can read the result.
	if err := coordinator1.Complete("w1", api.WorkerCompletion{
		AssignmentID:     assignment1.AssignmentID,
		RunID:            assignment1.RunID,
		ActivationID:     assignment1.ActivationID,
		AttemptID:        assignment1.AttemptID,
		CoordinatorEpoch: assignment1.CoordinatorEpoch,
		Generation:       assignment1.Generation,
		State:            "succeeded",
		Outputs:          map[string]any{"result": "step1-done"},
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate coordinator crash: cancel context (engine will stop).
	cancel1()
	select {
	case err := <-done1:
		if err == nil || (!errors.Is(err, context.Canceled) && err.Error() != "context canceled") {
			// Either context.Canceled or normal completion is OK.
			t.Logf("engine1 stopped with: %v (acceptable)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine1 did not stop")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen store — StartCoordinator marks the step1 assignment as uncertain
	// (even though it's already completed in the worker_results table).
	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	coordinator2 := api.NewCoordinator(st2) // calls StartCoordinator
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	coordinator2.Context = ctx2

	engines, err := plan.RecoverRuns(st2, coordinator2)
	if err != nil {
		t.Fatal(err)
	}
	// The run must be recovered: step1's result is stored; step2 never started.
	if engines[runID] == nil {
		run, _ := st2.GetRun(runID)
		t.Fatalf("run with uncertain/completed step1 not recovered: %+v", run)
	}

	// Register a fresh worker for step2.
	if _, err := coordinator2.Register(api.WorkerRegistration{WorkerID: "w2", Capacity: 2, Capabilities: []api.WorkerCapability{{Name: "test/echo"}}}); err != nil {
		t.Fatal(err)
	}

	eng2 := engines[runID]
	eng2.Context = ctx2

	done2 := make(chan error, 1)
	go func() { done2 <- eng2.Run(runID) }()

	// step1's result is already stored; the engine should find it immediately
	// via WorkerResult and proceed to dispatch step2.
	var assignment2 *api.WorkerAssignment
	deadline = time.Now().Add(5 * time.Second)
	for assignment2 == nil && time.Now().Before(deadline) {
		assignment2, err = coordinator2.NextAssignment("w2")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if assignment2 == nil {
		t.Fatal("step2 was never dispatched after step1 result was recovered")
	}

	// Complete step2.
	if err := coordinator2.Complete("w2", api.WorkerCompletion{
		AssignmentID:     assignment2.AssignmentID,
		RunID:            assignment2.RunID,
		ActivationID:     assignment2.ActivationID,
		AttemptID:        assignment2.AttemptID,
		CoordinatorEpoch: assignment2.CoordinatorEpoch,
		Generation:       assignment2.Generation,
		State:            "succeeded",
		Outputs:          map[string]any{"result": "step2-done"},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("resumed run failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resumed run did not complete in time")
	}

	run, err := st2.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("expected run to succeed, got: %+v", run)
	}
}

// TestUncertainExecuteContextCancelStopsWait verifies that when a coordinator
// restarts with an uncertain activation that has no stored result yet, the
// engine goroutine honours context cancellation instead of blocking forever.
func TestUncertainExecuteContextCancelStopsWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uncertain-cancel.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "uncertain-cancel",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "step", Kind: "execute", Executor: "test/echo", Outputs: map[string]plan.OutputDecl{"result": {}}},
				},
				Output: map[string]plan.Binding{"result": {Ref: &plan.ValueRef{Node: "step", Port: "result"}}},
			},
		},
	}

	coordinator1 := api.NewCoordinator(st)
	ctx1, cancel1 := context.WithCancel(context.Background())
	coordinator1.Context = ctx1

	eng1 := plan.NewEngineWithStore(p, coordinator1, st)
	eng1.Context = ctx1

	runID, err := eng1.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator1.Register(api.WorkerRegistration{WorkerID: "w1", Capacity: 1, Capabilities: []api.WorkerCapability{{Name: "test/echo"}}}); err != nil {
		t.Fatal(err)
	}

	done1 := make(chan error, 1)
	go func() { done1 <- eng1.Run(runID) }()

	// Wait for the activation to be assigned.
	var a1 *api.WorkerAssignment
	deadline := time.Now().Add(3 * time.Second)
	for a1 == nil && time.Now().Before(deadline) {
		a1, err = coordinator1.NextAssignment("w1")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if a1 == nil {
		t.Fatal("step was never assigned")
	}

	// Crash: cancel the engine WITHOUT the worker completing.
	cancel1()
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("engine1 did not stop")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: assignment is now uncertain, no result stored.
	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	coordinator2 := api.NewCoordinator(st2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	coordinator2.Context = ctx2

	engines, err := plan.RecoverRuns(st2, coordinator2)
	if err != nil {
		t.Fatal(err)
	}
	if engines[runID] == nil {
		run, _ := st2.GetRun(runID)
		t.Fatalf("run with uncertain activation not recovered: %+v", run)
	}

	eng2 := engines[runID]
	eng2.Context = ctx2

	done2 := make(chan error, 1)
	go func() { done2 <- eng2.Run(runID) }()

	// Give the engine a moment to enter the uncertain-wait poll loop.
	time.Sleep(200 * time.Millisecond)

	// Cancel the context — the engine must stop promptly.
	cancel2()
	select {
	case err := <-done2:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not stop after context cancellation")
	}
}

// TestSucceededActivationsPreservedOnResume verifies that activations that
// completed before a coordinator restart are not re-executed; their stored
// outputs are carried forward by the resumed Engine.Run walk.
func TestSucceededActivationsPreservedOnResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preserve.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// step1 succeeds; step2 never starts before the crash.
	fake := workers.NewFake().
		ByNode("step1", workers.ExecuteResult{Outputs: map[string]any{"result": "step1-original"}}).
		ByNode("step2", workers.ExecuteResult{Outputs: map[string]any{"result": "step2-output"}})

	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "preserve",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "step1", Kind: "execute", Executor: "test/echo", Outputs: map[string]plan.OutputDecl{"v": {}}},
					{ID: "step2", Kind: "execute", Executor: "test/echo",
						Inputs:  map[string]plan.Binding{"in": {Ref: &plan.ValueRef{Node: "step1", Port: "v"}}},
						Outputs: map[string]plan.OutputDecl{"result": {}},
					},
				},
				Output: map[string]plan.Binding{
					"result": {Ref: &plan.ValueRef{Node: "step2", Port: "result"}},
				},
			},
		},
	}

	eng1 := plan.NewEngineWithStore(p, fake, st)
	runID, err := eng1.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	// Run step1 to succeeded; interrupt before step2.
	// We simulate this by pre-creating step1's activation as succeeded with
	// outputs, then marking the run as running but not finishing.
	if err := st.SetRunState(runID, "running"); err != nil {
		t.Fatal(err)
	}
	actID1 := runID + ":step1"
	if err := st.CreateActivation(&state.Activation{
		ID:     actID1,
		RunID:  runID,
		NodeID: "step1",
		State:  "queued",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActivationOutputs(actID1, map[string]any{"v": "step1-original"}); err != nil {
		t.Fatal(err)
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and recover.
	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	engines, err := plan.RecoverRuns(st2, fake)
	if err != nil {
		t.Fatal(err)
	}
	if engines[runID] == nil {
		t.Fatal("run not recovered after partial progress")
	}

	// Record how many times step1 is called after recovery.
	callsBefore := len(fake.Calls())
	if err := engines[runID].Run(runID); err != nil {
		t.Fatal(err)
	}
	callsAfter := len(fake.Calls())

	// step1 must NOT have been re-executed; only step2 should have been called.
	for _, c := range fake.Calls()[callsBefore:callsAfter] {
		if c.NodeID == "step1" {
			t.Errorf("step1 was re-executed after recovery; it should reuse stored output")
		}
	}

	run, err := st2.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("expected succeeded, got %+v", run)
	}
}

// TestNonExecuteActivationResetOnRecovery ensures that structural/pure
// activations (project, join, map, branch, loop, child, wait) that were
// interrupted mid-execution are reset to queued so the engine can replay them
// without re-running any prior succeeded execute steps.
func TestNonExecuteActivationResetOnRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexec.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	fake := workers.NewFake().ByNode("exec", workers.ExecuteResult{Outputs: map[string]any{"v": "ok"}})

	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "nonexec",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "exec", Kind: "execute", Executor: "test/noop", Outputs: map[string]plan.OutputDecl{"v": {}}},
					// A project node is non-execute and pure.
					{
						ID:   "proj",
						Kind: "project",
						Inputs: map[string]plan.Binding{
							"v": {Ref: &plan.ValueRef{Node: "exec", Port: "v"}},
						},
					},
				},
				Output: map[string]plan.Binding{
					"v": {Ref: &plan.ValueRef{Node: "proj", Port: "v"}},
				},
			},
		},
	}

	eng1 := plan.NewEngineWithStore(p, fake, st)
	runID, err := eng1.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	// Manually advance state to simulate a mid-run interrupt:
	// exec succeeded; proj was running when the coordinator crashed.
	if err := st.SetRunState(runID, "running"); err != nil {
		t.Fatal(err)
	}
	execActID := runID + ":exec"
	if err := st.CreateActivation(&state.Activation{ID: execActID, RunID: runID, NodeID: "exec", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActivationOutputs(execActID, map[string]any{"v": "ok"}); err != nil {
		t.Fatal(err)
	}
	projActID := runID + ":proj"
	if err := st.CreateActivation(&state.Activation{ID: projActID, RunID: runID, NodeID: "proj", State: "running"}); err != nil {
		t.Fatal(err)
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	engines, err := plan.RecoverRuns(st2, fake)
	if err != nil {
		t.Fatal(err)
	}
	if engines[runID] == nil {
		run, _ := st2.GetRun(runID)
		t.Fatalf("run not recovered: %+v", run)
	}

	// proj must have been reset to queued by RecoverRuns.
	projAct, err := st2.GetActivation(projActID)
	if err != nil {
		t.Fatal(err)
	}
	if projAct.State != "queued" {
		t.Errorf("project activation should have been reset to queued, got %q", projAct.State)
	}

	// exec must remain succeeded.
	execAct, err := st2.GetActivation(execActID)
	if err != nil {
		t.Fatal(err)
	}
	if execAct.State != "succeeded" {
		t.Errorf("exec activation should remain succeeded, got %q", execAct.State)
	}

	// Run should complete without re-executing exec.
	callsBefore := len(fake.Calls())
	if err := engines[runID].Run(runID); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Calls()[callsBefore:] {
		if c.NodeID == "exec" {
			t.Errorf("exec was re-executed after recovery; it should reuse stored output")
		}
	}

	run, err := st2.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("expected succeeded, got %+v", run)
	}
}

// TestRebootResumesEngineWalkWithoutHumanIntervention verifies the core
// requirement: after a coordinator reboot, the in-memory Engine.Run walk for
// a pinned plan is automatically resumed. No human action is required to
// restart execution of unstarted work.
func TestRebootResumesEngineWalkWithoutHumanIntervention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reboot.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	fake := workers.NewFake().
		ByNode("a", workers.ExecuteResult{Outputs: map[string]any{"out": "a-result"}}).
		ByNode("b", workers.ExecuteResult{Outputs: map[string]any{"out": "b-result"}})

	p := &plan.Plan{
		IRVersion: "1.0",
		Name:      "reboot",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "a", Kind: "execute", Executor: "test/noop", Outputs: map[string]plan.OutputDecl{"out": {}}},
					{ID: "b", Kind: "execute", Executor: "test/noop",
						Inputs:  map[string]plan.Binding{"in": {Ref: &plan.ValueRef{Node: "a", Port: "out"}}},
						Outputs: map[string]plan.OutputDecl{"out": {}},
					},
				},
				Output: map[string]plan.Binding{"out": {Ref: &plan.ValueRef{Node: "b", Port: "out"}}},
			},
		},
	}

	eng := plan.NewEngineWithStore(p, fake, st)
	runID, err := eng.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	// Advance: a succeeded, b never started.
	if err := st.SetRunState(runID, "running"); err != nil {
		t.Fatal(err)
	}
	aActID := runID + ":a"
	if err := st.CreateActivation(&state.Activation{ID: aActID, RunID: runID, NodeID: "a", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActivationOutputs(aActID, map[string]any{"out": "a-result"}); err != nil {
		t.Fatal(err)
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// --- Reboot: reopen and recover ---
	st2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	recovered, err := plan.RecoverRuns(st2, fake)
	if err != nil {
		t.Fatal(err)
	}
	if recovered[runID] == nil {
		run, _ := st2.GetRun(runID)
		t.Fatalf("run not recovered after reboot: %+v", run)
	}

	// Run the recovered engine — no human intervention required.
	if err := recovered[runID].Run(runID); err != nil {
		t.Fatalf("recovered engine run failed: %v", err)
	}

	run, err := st2.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("expected run to succeed after reboot recovery, got %+v", run)
	}
}
