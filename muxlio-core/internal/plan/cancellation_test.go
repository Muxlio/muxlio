package plan

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type cancellationExecutor struct {
	started  chan workers.ExecuteRequest
	release  chan struct{}
	returned chan struct{}
}

func (x *cancellationExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	x.started <- req
	<-x.release // Deliberately models an external operation that ignores cancellation.
	close(x.returned)
	return workers.ExecuteResult{Outputs: map[string]any{"value": "late"}}, nil
}
func cancellationPlan() *Plan {
	depth := 2
	return &Plan{IRVersion: "1.0", Name: "cancellation", Root: "root", Subgraphs: map[string]Subgraph{
		"root":  {Nodes: []Node{{ID: "parent", Kind: KindChild, ChildRef: "child", MaxDepth: &depth}}},
		"child": {Nodes: []Node{{ID: "first", Kind: KindExecute, Executor: "test"}, {ID: "second", Kind: KindExecute, Executor: "test", Inputs: map[string]Binding{"after": {Ref: &ValueRef{Node: "first", Port: "value"}}}}}},
	}}
}
func TestCancelParentFencesLateResultsAndUndispatchedChildren(t *testing.T) {
	x := &cancellationExecutor{make(chan workers.ExecuteRequest, 2), make(chan struct{}), make(chan struct{})}
	engine := NewEngine(cancellationPlan(), x)
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run(id) }()
	req := <-x.started
	engine.Cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("uninterruptible executor blocked parent cancellation")
	}
	run, _ := engine.store.GetRun(id)
	if run.State != RunCancelled {
		t.Fatalf("run=%+v", run)
	}
	attempts, _ := engine.store.ListExecutionAttempts(id, "")
	if len(attempts) != 1 || attempts[0].State != "cancelled" {
		t.Fatalf("attempts=%+v", attempts)
	}
	select {
	case <-req.Context.Done():
	default:
		t.Fatal("descendant context not cancelled")
	}
	close(x.release)
	<-x.returned
	// Late direct commits are fenced by the store as well as the interpreter.
	if err := engine.store.SetActivationOutputs(req.ActivationID, map[string]any{"value": "late"}); err != nil {
		t.Fatal(err)
	}
	acts, _ := engine.store.ListActivations(id)
	for _, act := range acts {
		if act.State != ActCancelled || act.Outputs != nil || act.NodeID == "second" {
			t.Fatalf("activation=%+v", act)
		}
	}
	select {
	case <-x.started:
		t.Fatal("undispatched child started")
	default:
	}
}
func TestExpiredDeadlineSurvivesEngineReconstruction(t *testing.T) {
	p := cancellationPlan()
	seconds := 0.001
	p.DeadlineSeconds = &seconds
	store := state.New()
	first := NewEngineWithStore(p, workers.NewFake(), store)
	id, err := first.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// Persist a creation time old enough that recovery must not start a new window.
	time.Sleep(5 * time.Millisecond)
	fake := workers.NewFake()
	recovered := NewEngineWithStore(nil, fake, store)
	if err := recovered.Run(id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	run, _ := store.GetRun(id)
	if run.State != RunCancelled || len(fake.Calls()) != 0 {
		t.Fatalf("run=%+v calls=%v", run, fake.Calls())
	}
}
func TestExecutionTimeoutWithoutNodeTimeoutDoesNotPanic(t *testing.T) {
	e := NewEngine(&Plan{}, deadlineExecutor{})
	_, err := e.executeExecute("run", &Node{ID: "node", Executor: "test"}, nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "external effect completion unknown") {
		t.Fatalf("err=%v", err)
	}
}

type deadlineExecutor struct{}

func (deadlineExecutor) Execute(workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return workers.ExecuteResult{}, context.DeadlineExceeded
}

func TestExecutionTimeoutDoesNotReplayUninterruptibleEffect(t *testing.T) {
	x := &cancellationExecutor{make(chan workers.ExecuteRequest, 2), make(chan struct{}), make(chan struct{})}
	p := cancellationPlan()
	seconds := 0.005
	sg := p.Subgraphs["child"]
	sg.Nodes[0].TimeoutSeconds = &seconds
	sg.Nodes[0].Retry = &RetryPolicy{MaxAttempts: 3, Effect: "retryable"}
	p.Subgraphs["child"] = sg
	e := NewEngine(p, x)
	id, err := e.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(id) }()
	<-x.started
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout blocked on external operation")
	}
	if err == nil || !strings.Contains(err.Error(), "termination") || !strings.Contains(err.Error(), "node \"first\"") {
		t.Fatalf("missing actionable diagnostic: %v", err)
	}
	run, _ := e.store.GetRun(id)
	if run.State != RunFailed {
		t.Fatalf("run=%+v", run)
	}
	attempts, _ := e.store.ListExecutionAttempts(id, "")
	if len(attempts) != 1 {
		t.Fatalf("timeout replayed: %+v", attempts)
	}
	close(x.release)
	<-x.returned
}
