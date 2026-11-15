package plan

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestAnyAgentPrefersDifferentDependencyExecutor(t *testing.T) {
	s := state.New()
	for i := 0; i < 5; i++ {
		if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: fmt.Sprintf("b-%d", i), RunID: "run", ActivationID: fmt.Sprintf("run:b-%d", i), Executor: "agent/b", State: "succeeded", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "writer", RunID: "run", ActivationID: "run:writer", Executor: "agent/a", State: "succeeded", StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	x := workers.NewFake().ByExecutor("agent/a", workers.ExecuteResult{}).ByExecutor("agent/b", workers.ExecuteResult{})
	e := NewEngineWithStore(&Plan{}, x, s)
	node := &Node{ID: "review", Executor: "any", Inputs: map[string]Binding{"__after_writer": {Ref: &ValueRef{Node: "writer", Port: "result"}}}}
	if _, err := e.executeExecute("run", node, nil, &EvalContext{Outputs: map[string]map[string]any{"writer": {"result": "text"}}}); err != nil {
		t.Fatal(err)
	}
	if calls := x.Calls(); len(calls) != 1 || calls[0].Executor != "agent/b" {
		t.Fatalf("review repeated writer despite another agent: %+v", calls)
	}
}

func TestLocalAnyAgentConcurrentAdmissionUsesCurrentLoad(t *testing.T) {
	x := workers.NewFake().ByExecutor("agent/a", workers.ExecuteResult{}).ByExecutor("agent/b", workers.ExecuteResult{})
	e := NewEngine(&Plan{}, x)
	var wg sync.WaitGroup
	chosen := make(chan string, 2)
	errors := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			req, _, err := e.prepareExecutionAttempt(workers.ExecuteRequest{RunID: "run", ActivationID: "run:" + id, AttemptID: id, Executor: "any"}, &Node{ID: id}, nil, nil, "", 1)
			chosen <- req.Executor
			errors <- err
		}(id)
	}
	wg.Wait()
	close(chosen)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	names := map[string]bool{}
	for name := range chosen {
		names[name] = true
	}
	if len(names) != 2 {
		t.Fatalf("concurrent local selection ignored active attempts: %v", names)
	}
}

type keyedSelectionExecutor struct {
	calls      []workers.ExecuteRequest
	selections int
	uncertain  bool
}

func (x *keyedSelectionExecutor) SelectExecutor(req workers.ExecuteRequest, usage map[string]int) (string, error) {
	x.selections++
	return workers.SelectAgent([]string{"agent/a", "agent/b"}, usage)
}

func (x *keyedSelectionExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	x.calls = append(x.calls, req)
	if len(x.calls) == 1 {
		if x.uncertain {
			return workers.ExecuteResult{}, workers.UncertainExecutionError{}
		}
		return workers.ExecuteResult{Error: "temporary failure"}, nil
	}
	return workers.ExecuteResult{}, nil
}

func TestAnyAgentKeyedRetriesKeepDeduplicationDomain(t *testing.T) {
	for _, effect := range []string{"idempotency-key", "idempotent-with-key"} {
		for _, uncertain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/uncertain=%t", effect, uncertain), func(t *testing.T) {
				x := &keyedSelectionExecutor{uncertain: uncertain}
				e := NewEngine(&Plan{}, x)
				_, err := e.executeExecute("run", &Node{ID: "step", Executor: "any", Retry: &RetryPolicy{Effect: effect, MaxAttempts: 2}}, nil, &EvalContext{})
				if err != nil {
					t.Fatal(err)
				}
				if len(x.calls) != 2 || x.selections != 1 {
					t.Fatalf("calls=%+v selections=%d", x.calls, x.selections)
				}
				for _, call := range x.calls {
					if call.Executor != "agent/a" || call.IdempotencyKey != "run:step" {
						t.Fatalf("keyed operation changed identity: %+v", call)
					}
				}
			})
		}
	}
}

func TestAnyAgentKeyedRecoveryKeepsPersistedExecutor(t *testing.T) {
	for _, status := range []string{"running", "failed"} {
		t.Run(status, func(t *testing.T) {
			s := state.New()
			if err := s.CreateActivation(&state.Activation{ID: "run:step", RunID: "run", NodeID: "step", State: "running", AttemptBase: 1}); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "run:step-attempt-1", RunID: "run", ActivationID: "run:step", RetryNumber: 1, Executor: "agent/b", State: status, Effect: "idempotent-with-key", IdempotencyKey: "original-key"}); err != nil {
				t.Fatal(err)
			}
			x := &keyedSelectionExecutor{}
			e := NewEngineWithStore(&Plan{}, x, s)
			_, err := e.executeExecute("run", &Node{ID: "step", Executor: "agent/any", Retry: &RetryPolicy{Effect: "idempotent-with-key", MaxAttempts: 2}}, nil, &EvalContext{})
			if err != nil {
				t.Fatal(err)
			}
			if x.selections != 0 || len(x.calls) != 2 {
				t.Fatalf("calls=%+v selections=%d", x.calls, x.selections)
			}
			for _, call := range x.calls {
				if call.Executor != "agent/b" || call.IdempotencyKey != "original-key" || call.AttemptID == "run:step-attempt-1" {
					t.Fatalf("recovery changed keyed identity: %+v", call)
				}
			}
		})
	}
}

func TestAnyAgentPersistsConcreteChoicesAcrossSteps(t *testing.T) {
	for _, count := range []int{1, 2} {
		x := workers.NewFake().ByExecutor("agent/a", workers.ExecuteResult{})
		if count == 2 {
			x.ByExecutor("agent/b", workers.ExecuteResult{})
		}
		e := NewEngine(&Plan{}, x)
		for _, id := range []string{"first", "second", "third"} {
			if _, err := e.executeExecute("run", &Node{ID: id, Executor: "agent/any"}, nil, &EvalContext{}); err != nil {
				t.Fatal(err)
			}
		}
		calls := x.Calls()
		if calls[0].Executor != "agent/a" || calls[2].Executor != "agent/a" {
			t.Fatalf("choices = %+v", calls)
		}
		if count == 2 && calls[1].Executor != "agent/b" {
			t.Fatalf("no variety: %+v", calls)
		}
		attempts, err := e.Store().ListExecutionAttempts("run", "")
		if err != nil || len(attempts) != 3 {
			t.Fatalf("attempts = %+v, %v", attempts, err)
		}
		for _, a := range attempts {
			if workers.IsAnyAgent(a.Executor) {
				t.Fatalf("unresolved attempt: %+v", a)
			}
		}
	}
}

type selectingQueuedExecutor struct {
	*workers.FakeExecutor
	selections int
}

func (x *selectingQueuedExecutor) SelectExecutor(req workers.ExecuteRequest, usage map[string]int) (string, error) {
	x.selections++
	return x.FakeExecutor.SelectExecutor(req, usage)
}
func (x *selectingQueuedExecutor) ExecuteQueued(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return x.Execute(req)
}

func TestAnyAgentRecoveryReusesPersistedChoice(t *testing.T) {
	s := state.New()
	prior := state.ExecutionAttempt{ID: "run:step-attempt-1", RunID: "run", ActivationID: "run:step", RetryNumber: 1, Executor: "agent/b", State: "queued", StartedAt: time.Now()}
	if err := s.RecordExecutionAttempt(prior); err != nil {
		t.Fatal(err)
	}
	x := &selectingQueuedExecutor{FakeExecutor: workers.NewFake().ByExecutor("agent/a", workers.ExecuteResult{})}
	e := NewEngineWithStore(&Plan{}, x, s)
	if _, err := e.executeExecute("run", &Node{ID: "step", Executor: "any"}, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	if x.selections != 0 || len(x.Calls()) != 1 || x.Calls()[0].Executor != "agent/b" {
		t.Fatalf("reselected recovered work: %d %+v", x.selections, x.Calls())
	}
}

func TestAnyAgentFailsWithoutCandidates(t *testing.T) {
	x := workers.NewFake().ByExecutor("command", workers.ExecuteResult{})
	e := NewEngine(&Plan{}, x)
	if _, err := e.executeExecute("run", &Node{ID: "step", Executor: "any"}, nil, &EvalContext{}); err == nil || len(x.Calls()) != 0 {
		t.Fatalf("executed without an agent: %v %+v", err, x.Calls())
	}
}

func TestAnyAgentRetryChoosesAnotherAgent(t *testing.T) {
	x := workers.NewFake().ByExecutor("agent/a", workers.ExecuteResult{Error: "temporary failure"}).ByExecutor("agent/b", workers.ExecuteResult{})
	e := NewEngine(&Plan{}, x)
	if _, err := e.executeExecute("run", &Node{ID: "step", Executor: "any", Retry: &RetryPolicy{Effect: "retryable", MaxAttempts: 2}}, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	calls := x.Calls()
	if len(calls) != 2 || calls[0].Executor != "agent/a" || calls[1].Executor != "agent/b" {
		t.Fatalf("retry choices = %+v", calls)
	}
}
