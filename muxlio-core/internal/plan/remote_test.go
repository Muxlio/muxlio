package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
			if req.IdempotencyKey == "" {
				return workers.ExecuteResult{}, fmt.Errorf("missing submission identity")
			}
			return workers.ExecuteResult{RemoteTask: &contract.RemoteTaskReference{ID: "remote-1", Pending: true, PollAt: pollAt}}, nil
		}
		polls.Add(1)
		if req.Action != "poll" || req.RemoteTask.ID != "remote-1" || req.IdempotencyKey != "run:node" || req.OperationAttemptID != "run:node-attempt-1" || len(req.ConcurrencyKeys) != 0 || req.Workspace != "" || req.TimeoutSeconds == nil {
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
	if active.Load() != 0 || polls.Load() != 0 || detached.OperationRef.ID != "remote-1" || !detached.OperationRef.PollAt.Equal(pollAt) || detached.IdempotencyKey != "run:node" {
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
	outputs, err := e.executeExecute("run", remoteNode(), nil, &EvalContext{})
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
