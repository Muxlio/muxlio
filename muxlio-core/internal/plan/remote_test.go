package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type remoteExecutor struct {
	execute func(workers.ExecuteRequest) (workers.ExecuteResult, error)
}

func (f remoteExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return f.execute(req)
}

func remoteNode() *Node {
	return &Node{ID: "node", Executor: "test", RequiredCapabilities: map[string]string{contract.CapabilityRemoteTask: "1"}, ConcurrencyKeys: []string{"resource"}}
}

func TestRemoteHourWaitReleasesExecutorAndResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	var starts atomic.Int32
	var polls atomic.Int32
	pollAt := time.Now().Add(time.Hour).UTC()
	f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		if active.Add(1) != 1 {
			return workers.ExecuteResult{}, fmt.Errorf("single executor slot is occupied")
		}
		defer active.Add(-1)
		if req.NodeID == "other" {
			return workers.ExecuteResult{}, nil
		}
		if req.Action == "" {
			starts.Add(1)
			manifest := &contract.AdapterManifest{Executor: "test", ContractVersion: contract.Version, Capabilities: map[string]bool{contract.CapabilityRemoteTask: true, contract.CapabilityReconcile: true}}
			if err := workers.CheckAdapterCapabilities(req, manifest); err != nil {
				return workers.ExecuteResult{}, err
			}
			return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "remote-1", ContextID: "context-1", Pending: true, PollAt: pollAt}}, nil
		}
		polls.Add(1)
		if req.Action != "poll" || req.RemoteTask.ID != "remote-1" || req.RemoteTask.ContextID != "context-1" || req.IdempotencyKey != "" || req.OperationAttemptID != "run:node-attempt-1" || len(req.ConcurrencyKeys) != 0 || req.Workspace != "" || req.TimeoutSeconds == nil {
			return workers.ExecuteResult{}, fmt.Errorf("invalid poll: %+v", req)
		}
		return workers.ExecuteResult{Outputs: map[string]any{"answer": "done"}}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	e := NewEngineWithStore(&Plan{}, f, s)
	e.Context = ctx
	done := make(chan error, 1)
	go func() {
		_, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
		done <- err
	}()
	var detached state.ExecutionAttempt
	deadline := time.After(5 * time.Second)
	for detached.State != "detached" {
		select {
		case err := <-done:
			t.Fatalf("remote operation returned early: %v", err)
		case <-deadline:
			t.Fatal("handoff was not persisted")
		default:
		}
		attempts, err := s.ListExecutionAttempts("run", "run:node")
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) > 0 {
			detached = attempts[0]
		}
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 0 || polls.Load() != 0 || detached.OperationRef.ID != "remote-1" || detached.OperationRef.ContextID != "context-1" || !detached.OperationRef.PollAt.Equal(pollAt) || detached.IdempotencyKey != "" {
		t.Fatalf("invalid durable handoff: %+v; active=%d polls=%d", detached, active.Load(), polls.Load())
	}
	if _, err := e.executeExecute("run", &Node{ID: "other", Executor: "test"}, nil, &EvalContext{}); err != nil {
		t.Fatalf("unrelated work could not use the released slot: %v", err)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("shutdown = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Advance the persisted schedule instead of spending an hour in the test.
	attempts, err := s.ListExecutionAttempts("run", "run:node")
	if err != nil || len(attempts) != 1 || attempts[0].State != "detached" {
		t.Fatalf("restart lost handoff: %+v %v", attempts, err)
	}
	detached = attempts[0]
	detached.OperationRef.PollAt = time.Now().Add(-time.Second)
	if err := s.RecordExecutionAttempt(detached); err != nil {
		t.Fatal(err)
	}
	e = NewEngineWithStore(&Plan{}, f, s)
	outputs, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
	if err != nil || outputs["answer"] != "done" || starts.Load() != 1 || polls.Load() != 1 {
		t.Fatalf("restart resubmitted or lost result: %v %v starts=%d polls=%d", outputs, err, starts.Load(), polls.Load())
	}
	if _, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{}); err != nil || polls.Load() != 1 {
		t.Fatalf("completed remote operation was polled again: %v", err)
	}
}

func TestAmbiguousRemoteSubmissionReconcilesBeforePolling(t *testing.T) {
	var actions []string
	f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		actions = append(actions, req.Action)
		switch req.Action {
		case "":
			return workers.ExecuteResult{}, fmt.Errorf("connection lost after submission")
		case "reconcile":
			if req.IdempotencyKey != "run:node" || req.OperationAttemptID != "run:node-attempt-1" {
				t.Fatal("reconciliation lost submission identity")
			}
			return workers.ExecuteResult{Reconciliation: &contract.Reconciliation{Status: "pending"}, RemoteTask: &contract.RemoteTaskReference{ID: "found-by-key", Pending: true, PollAt: time.Now().Add(-time.Second)}}, nil
		case "poll":
			if req.RemoteTask.ID != "found-by-key" {
				t.Fatal("poll lost reconciled identity")
			}
			return workers.ExecuteResult{Outputs: map[string]any{"answer": true}}, nil
		}
		panic("unexpected action")
	}}
	e := NewEngine(&Plan{}, f)
	node := remoteNode()
	node.Retry = &RetryPolicy{Effect: "idempotency-key", IdempotencyKey: "run:node"}
	outputs, err := e.executeExecute("run", node, nil, &EvalContext{})
	if err != nil || outputs["answer"] != true || fmt.Sprint(actions) != "[ reconcile poll]" {
		t.Fatalf("ambiguous submission replayed: %v %v %v", outputs, err, actions)
	}
}

func TestUnrequestedRemoteHandoffFailsClosed(t *testing.T) {
	f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "remote", Pending: true}}, nil
	}}
	e := NewEngine(&Plan{}, f)
	if _, err := e.executeExecute("run", &Node{ID: "node", Executor: "test"}, nil, &EvalContext{}); err == nil {
		t.Fatal("accepted an unrequested handoff")
	}
	attempts, err := e.store.ListExecutionAttempts("run", "run:node")
	if err != nil || len(attempts) != 1 || attempts[0].State != "awaiting_reconciliation" {
		t.Fatalf("unsafe handoff not held: %+v %v", attempts, err)
	}
}

func TestUncertainPollDoesNotCompleteOrResubmitRemoteJob(t *testing.T) {
	var submissions atomic.Int32
	f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		if req.Action == "" {
			submissions.Add(1)
			return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "remote", Pending: true, PollAt: time.Now().Add(-time.Second)}}, nil
		}
		return workers.ExecuteResult{}, workers.UncertainExecutionError{}
	}}
	e := NewEngine(&Plan{}, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e.Context = ctx
	done := make(chan error, 1)
	go func() {
		_, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
		done <- err
	}()
	for {
		select {
		case err := <-done:
			t.Fatalf("uncertain poll ended remote operation: %v", err)
		default:
		}
		queries, err := e.store.ListExecutionAttempts("run", "run:node-attempt-1-poll")
		if err != nil {
			t.Fatal(err)
		}
		if len(queries) > 0 && queries[0].State == "awaiting_reconciliation" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("shutdown = %v", err)
	}
	attempts, err := e.store.ListExecutionAttempts("run", "run:node")
	if err != nil || len(attempts) != 1 || attempts[0].State != "detached" || submissions.Load() != 1 {
		t.Fatalf("uncertain lookup changed submission: %+v %v submissions=%d", attempts, err, submissions.Load())
	}
}

func TestFailedRemotePollPersistsRetryDelay(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			var polls atomic.Int32
			f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
				if req.Action == "" {
					return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "remote", Pending: true, PollAt: time.Now().Add(-time.Second)}}, nil
				}
				polls.Add(1)
				if uncertain {
					return workers.ExecuteResult{}, workers.UncertainExecutionError{}
				}
				return workers.ExecuteResult{Error: "lookup unavailable"}, nil
			}}
			e := NewEngine(&Plan{}, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.Context = ctx
			done := make(chan error, 1)
			go func() {
				_, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
				done <- err
			}()
			defer func() { cancel(); <-done }()
			deadline := time.After(5 * time.Second)
			for {
				attempts, err := e.store.ListExecutionAttempts("run", "run:node")
				if err != nil {
					t.Fatal(err)
				}
				if len(attempts) == 1 && attempts[0].OperationRef != nil && attempts[0].OperationRef.PollAt.After(time.Now().Add(20*time.Second)) {
					if attempts[0].State != "detached" || polls.Load() != 1 {
						t.Fatalf("failed lookup did not remain detached with one poll: %+v polls=%d", attempts, polls.Load())
					}
					break
				}
				select {
				case <-deadline:
					t.Fatalf("failed poll did not checkpoint a future retry; polls=%d", polls.Load())
				case <-time.After(time.Millisecond):
				}
			}
		})
	}
}

func TestRemotePollNarrowsWorkspacePolicyToScratch(t *testing.T) {
	for _, filesystem := range []string{"workspace-read-only", "workspace-write"} {
		t.Run(filesystem, func(t *testing.T) {
			node := remoteNode()
			node.WorkspaceMode = "read-only"
			if filesystem == "workspace-write" {
				node.WorkspaceMode = "shared-write"
			}
			node.Params = json.RawMessage(fmt.Sprintf(`{"executionPolicy":{"trust":"restricted","filesystem":%q},"isolation":{"mode":"required"},"jobOption":true}`, filesystem))
			original := string(node.Params)
			polls := 0
			f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
				if req.Action == "" {
					return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "remote", Pending: true, PollAt: time.Now().Add(-time.Second)}}, nil
				}
				polls++
				if _, err := workers.GuardRequest(req); err != nil {
					t.Errorf("poll policy rejected: %v", err)
				}
				if req.Policy.Filesystem != "scratch" || req.Policy.Trust != "restricted" || req.WorkspaceMode != "" || len(req.ConcurrencyKeys) != 0 {
					t.Errorf("poll retained workspace permissions: %+v", req)
				}
				var params struct {
					JobOption bool `json:"jobOption"`
				}
				if err := json.Unmarshal(req.Params, &params); err != nil || !params.JobOption {
					t.Errorf("poll lost job configuration: %s %v", req.Params, err)
				}
				return workers.ExecuteResult{}, nil
			}}
			e := NewEngine(&Plan{}, f)
			if _, err := e.executeExecute("run", node, nil, &EvalContext{}); err != nil || polls != 1 || string(node.Params) != original {
				t.Fatalf("poll failed or mutated the node: %v polls=%d params=%s", err, polls, node.Params)
			}
		})
	}
}

func TestTerminalRemoteFailureEndsPollingWithoutResubmission(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(fmt.Sprint(reconcile), func(t *testing.T) {
			var starts, polls int
			f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
				switch req.Action {
				case "":
					starts++
					if reconcile {
						return workers.ExecuteResult{}, workers.UncertainExecutionError{}
					}
					return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "remote", Pending: true, PollAt: time.Now().Add(-time.Second)}}, nil
				case "poll", "reconcile":
					polls++
					result := workers.ExecuteResult{Error: "remote operation rejected", RemoteTask: &contract.RemoteTaskReference{ID: "remote"}}
					if req.Action == "reconcile" {
						result.Reconciliation = &contract.Reconciliation{Status: "completed"}
					}
					return result, nil
				}
				t.Fatalf("unexpected action: %q", req.Action)
				return workers.ExecuteResult{}, nil
			}}
			e := NewEngine(&Plan{}, f)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e.Context = ctx
			_, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
			if err == nil || !strings.Contains(err.Error(), "remote operation rejected") || starts != 1 || polls != 1 {
				t.Fatalf("terminal failure: %v starts=%d polls=%d", err, starts, polls)
			}
			attempts, err := e.store.ListExecutionAttempts("run", "run:node")
			if err != nil || len(attempts) != 1 || attempts[0].State != "failed" {
				t.Fatalf("failure not durable: %+v %v", attempts, err)
			}
		})
	}
}

func TestRecoveredReconciliationPreservesTerminalRemoteFailure(t *testing.T) {
	f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		if req.Action != "reconcile" || req.RemoteTask == nil || req.RemoteTask.ID != "remote" {
			t.Fatalf("recovery repeated submission or lost identity: %+v", req)
		}
		return workers.ExecuteResult{Error: "remote operation rejected", RemoteTask: &contract.RemoteTaskReference{ID: "remote"}, Reconciliation: &contract.Reconciliation{Status: "completed"}}, nil
	}}
	e := NewEngine(&Plan{}, f)
	if err := e.store.RecordExecutionAttempt(state.ExecutionAttempt{ID: "run:node-attempt-1", RunID: "run", ActivationID: "run:node", Executor: "test", RetryNumber: 1, State: "running", Effect: "reconcilable", OperationRef: &contract.RemoteTaskReference{ID: "remote"}}); err != nil {
		t.Fatal(err)
	}
	_, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "remote operation rejected") {
		t.Fatalf("recovered failure became success: %v", err)
	}
	attempts, err := e.store.ListExecutionAttempts("run", "run:node")
	if err != nil || len(attempts) != 1 || attempts[0].State != "failed" || attempts[0].AwaitReconciliationReason != "" {
		t.Fatalf("terminal failure not durable: %+v %v", attempts, err)
	}
}

func TestSubmissionCanConfirmTerminalRemoteFailure(t *testing.T) {
	calls := 0
	f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		calls++
		if req.Action != "" {
			t.Fatalf("confirmed outcome unnecessarily reconciled: %s", req.Action)
		}
		return workers.ExecuteResult{Error: "remote operation rejected", RemoteTask: &contract.RemoteTaskReference{ID: "remote"}}, nil
	}}
	e := NewEngine(&Plan{}, f)
	_, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "remote operation rejected") || calls != 1 {
		t.Fatalf("terminal submission: %v calls=%d", err, calls)
	}
}

func TestReconciliationNarrowsWorkspacePolicyToScratch(t *testing.T) {
	for _, filesystem := range []string{"workspace-read-only", "workspace-write"} {
		t.Run(filesystem, func(t *testing.T) {
			params := json.RawMessage(fmt.Sprintf(`{"executionPolicy":{"trust":"restricted","filesystem":%q},"isolation":{"mode":"required"},"jobOption":true}`, filesystem))
			mode := "read-only"
			if filesystem == "workspace-write" {
				mode = "shared-write"
			}
			req, err := workers.GuardRequest(workers.ExecuteRequest{Executor: "test", Workspace: t.TempDir(), WorkspaceMode: mode, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			f := remoteExecutor{execute: func(lookup workers.ExecuteRequest) (workers.ExecuteResult, error) {
				if _, err := workers.GuardRequest(lookup); err != nil {
					t.Fatalf("reconciliation policy rejected: %v", err)
				}
				if lookup.Action != "reconcile" || lookup.Policy.Filesystem != "scratch" || lookup.Workspace != "" || lookup.WorkspaceMode != "" {
					t.Fatalf("invalid lookup: %+v", lookup)
				}
				var config struct {
					JobOption bool `json:"jobOption"`
				}
				if err := json.Unmarshal(lookup.Params, &config); err != nil || !config.JobOption {
					t.Fatalf("lost job parameters: %s", lookup.Params)
				}
				return workers.ExecuteResult{Reconciliation: &contract.Reconciliation{Status: "completed"}}, nil
			}}
			e := NewEngine(&Plan{}, f)
			a := state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "activation", Executor: "test", Effect: "reconcilable"}
			if _, _, err := e.reconcileEffect(req, a, &Node{ID: "node"}); err != nil {
				t.Fatal(err)
			}
			if req.Policy.Filesystem != filesystem || string(req.Params) != string(params) {
				t.Fatal("lookup mutated submission policy")
			}
		})
	}
}

func TestRemoteFailureDiagnosticsRedactSecrets(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(fmt.Sprint(reconcile), func(t *testing.T) {
			const secret = "private-remote-credential"
			f := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
				result := workers.ExecuteResult{Error: "remote rejected " + secret, RemoteTask: &contract.RemoteTaskReference{ID: "remote"}}
				if reconcile {
					result.Reconciliation = &contract.Reconciliation{Status: "completed"}
				}
				return result, nil
			}}
			e := NewEngine(&Plan{}, f)
			req := workers.ExecuteRequest{Executor: "test", Secrets: map[string]string{"TOKEN": secret}, RequiredCapabilities: map[string]string{contract.CapabilityRemoteTask: "1"}}
			a := state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "activation", Executor: "test", Effect: "reconcilable", OperationRef: &contract.RemoteTaskReference{ID: "remote", Pending: true, PollAt: time.Now().Add(-time.Second)}}
			if reconcile {
				if _, _, err := e.reconcileEffect(req, a, &Node{ID: "node"}); err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := e.pollRemote(req, a, &Node{ID: "node"})
				if err == nil || strings.Contains(err.Error(), secret) {
					t.Fatalf("unsafe poll error: %v", err)
				}
			}
			attempts, err := e.store.ListExecutionAttempts("run", "")
			if err != nil || len(attempts) != 2 {
				t.Fatalf("missing attempts: %+v %v", attempts, err)
			}
			for _, attempt := range attempts {
				if strings.Contains(attempt.Failure, secret) || strings.Contains(attempt.AwaitReconciliationReason, secret) {
					t.Fatalf("credential persisted: %+v", attempt)
				}
			}
		})
	}
}
