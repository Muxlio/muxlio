package plan

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestExecutorContractRequirementsReachDispatch(t *testing.T) {
	var node Node
	if err := json.Unmarshal([]byte(`{"id":"task","kind":"execute","executor":"test","executorVersion":"2","contractVersion":"1","requiredCapabilities":{"progress":"1"},"session":{"mode":"create"},"remoteTask":{"id":"task","url":"https://example.test/task"}}`), &node); err != nil {
		t.Fatal(err)
	}
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "run:task", RunID: "run", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	handle := &contract.SessionHandle{ID: "opaque", Executor: "test", RunnerVersion: "runner-1", OwnerWorker: "worker", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour).UTC(), Recoverable: true}
	executor := workers.NewFake().ByNode("task", workers.ExecuteResult{Session: handle})
	engine := NewEngineWithStore(&Plan{}, executor, store)
	if _, err := engine.executeExecute("run", &node, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListExecutionAttempts("run", "run:task")
	if err != nil || len(attempts) != 1 || attempts[0].Result == nil || !reflect.DeepEqual(attempts[0].Result.Session, handle) {
		t.Fatalf("session not persisted: %+v %v", attempts, err)
	}
	calls := executor.Calls()
	if len(calls) != 1 {
		t.Fatalf("dispatches: %d", len(calls))
	}
	req := calls[0]
	if req.ExecutorVersion != node.ExecutorVersion || req.ContractVersion != node.ContractVersion || !reflect.DeepEqual(req.Session, node.Session) || !reflect.DeepEqual(req.RequiredCapabilities, node.RequiredCapabilities) || !reflect.DeepEqual(req.RemoteTask, node.RemoteTask) {
		t.Fatalf("contract requirements lost: %+v", req)
	}
}

func TestUncertainSessionPromptDoesNotReplayOrFallback(t *testing.T) {
	for _, effect := range []string{"pure", "idempotent-with-key", "reconcilable"} {
		t.Run(effect, func(t *testing.T) {
			store := state.New()
			if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateActivation(&state.Activation{ID: "run:node", RunID: "run", NodeID: "node", State: "running"}); err != nil {
				t.Fatal(err)
			}
			executor := &effectExecutor{}
			engine := NewEngineWithStore(&Plan{}, executor, store)
			node := &Node{ID: "node", Executor: "test", Session: &contract.SessionRequest{Mode: "create"}, FallbackExecutors: []string{"other"}, Retry: &RetryPolicy{Effect: effect, MaxAttempts: 2}}
			if _, err := engine.executeExecute("run", node, nil, &EvalContext{}); err == nil {
				t.Fatal("uncertain session accepted")
			}
			if len(executor.calls) != 1 {
				t.Fatalf("session replayed or reconciled: %+v", executor.calls)
			}
		})
	}
}

func TestSessionOutputValidationDoesNotReplayPrompt(t *testing.T) {
	executor := workers.NewFake().ByNode("task", workers.ExecuteResult{Outputs: map[string]any{"answer": 42}})
	engine := NewEngine(&Plan{}, executor)
	node := &Node{ID: "task", Executor: "test", Session: &contract.SessionRequest{Mode: "create"},
		Retry:   &RetryPolicy{Effect: "pure", MaxAttempts: 3},
		Outputs: map[string]OutputDecl{"answer": {Schema: json.RawMessage(`{"type":"string"}`)}},
	}
	if _, err := engine.executeExecute("run", node, nil, &EvalContext{}); err == nil {
		t.Fatal("invalid session output accepted")
	}
	if calls := executor.Calls(); len(calls) != 1 {
		t.Fatalf("output validation replayed a mutable session prompt: %d calls", len(calls))
	}
}

func TestSessionExecutionFailuresDoNotReplayPrompt(t *testing.T) {
	for _, mode := range []string{"create", "resume"} {
		for _, failure := range []string{"executor", "result"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				executor := workers.NewFake()
				if failure == "executor" {
					executor.FailOnNode("task", errors.New("connection lost after submission"), 0)
				} else {
					executor.ByNode("task", workers.ExecuteResult{Error: "turn failed after advancing conversation"})
				}
				session := &contract.SessionRequest{Mode: mode}
				if mode == "resume" {
					session.Handle = &contract.SessionHandle{ID: "opaque", Executor: "test", RunnerVersion: "runner-1", OwnerWorker: "owner", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour)}
				}
				engine := NewEngine(&Plan{}, executor)
				node := &Node{ID: "task", Executor: "test", Session: session, Retry: &RetryPolicy{Effect: "pure", MaxAttempts: 3}}
				if _, err := engine.executeExecute("run", node, nil, &EvalContext{}); err == nil {
					t.Fatal("failed session accepted")
				}
				if calls := executor.Calls(); len(calls) != 1 {
					t.Fatalf("execution failure replayed a mutable session prompt: %d calls", len(calls))
				}
			})
		}
	}
}
