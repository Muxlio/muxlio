package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestLostSessionReportsUncertaintyWhenOwnerIsOffline(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	manifest := &contract.AdapterManifest{Executor: "test", Capabilities: map[string]bool{contract.CapabilitySession: true}, CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}}
	worker := state.Worker{ID: "owner", Capacity: 1, LastSeen: time.Now(), Capabilities: []state.WorkerCapability{{Name: "test", Contract: manifest}}, Labels: map[string]string{workers.SessionEnvironmentLabel: "env"}}
	if err := store.RegisterWorker(worker); err != nil {
		t.Fatal(err)
	}
	req := workers.ExecuteRequest{RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Session: &contract.SessionRequest{Mode: "create"}}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "attempt", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	if d, _, err := store.ClaimDispatchForEpoch("owner", time.Now().Add(-time.Second), c.epoch); err != nil || d == nil {
		t.Fatalf("claim: %+v %v", d, err)
	}
	worker.LastSeen = time.Now().Add(-2 * WorkerHeartbeatWindow)
	if err := store.RegisterWorker(worker); err != nil {
		t.Fatal(err)
	}
	_, err = c.Execute(req)
	var uncertain workers.UncertainExecutionError
	if !errors.As(err, &uncertain) {
		t.Fatalf("lost session must require explicit resolution, got %v", err)
	}
}

func TestDisconnectedWorkerLeaseBecomesUncertainWithoutAnotherPoll(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	c.lease = 20 * time.Millisecond
	if _, err = c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Execute(workers.ExecuteRequest{Context: ctx, RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test"})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	var assignment *WorkerAssignment
	for time.Now().Before(deadline) {
		assignment, err = c.NextAssignment("worker")
		if err != nil {
			t.Fatal(err)
		}
		if assignment != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if assignment == nil {
		t.Fatal("no assignment")
	}
	// Simulate a vanished worker: no heartbeats, completions, or further polls.
	// After the lease expires Execute must keep polling for reconciliation rather
	// than enqueueing another dispatch.
	time.Sleep(c.lease + 5*time.Millisecond)
	// Reconnection does not restore authority, even if the process registers
	// again and reports its old lease and a successful result.
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Heartbeat("worker", WorkerHeartbeat{Leases: []state.AssignmentLease{{AssignmentID: assignment.AssignmentID, Generation: assignment.Generation, CoordinatorEpoch: assignment.CoordinatorEpoch}}}); err != nil {
		t.Fatal(err)
	}
	// Late completions may be acknowledged as diagnostics, but cannot publish
	// the worker result that the interpreter consumes.
	if err := c.Complete("worker", WorkerCompletion{AssignmentID: assignment.AssignmentID, RunID: assignment.RunID, ActivationID: assignment.ActivationID, AttemptID: assignment.AttemptID, CoordinatorEpoch: assignment.CoordinatorEpoch, Generation: assignment.Generation, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := store.WorkerResult(assignment.AssignmentID); err != nil || len(raw) != 0 {
		t.Fatalf("stale result became authoritative: %s %v", raw, err)
	}
	select {
	case err := <-done:
		t.Fatalf("Execute returned before reconciliation wait ended: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lease expiry wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not stop after context cancellation")
	}
}

func TestDispatchReturnsDurableResultAndRenewsLease(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "test"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "activation", Executor: "test", State: "running"}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan workers.ExecuteResult, 1)
	errs := make(chan error, 1)
	go func() {
		result, err := c.Execute(workers.ExecuteRequest{RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test"})
		done <- result
		errs <- err
	}()
	var a *WorkerAssignment
	deadline := time.Now().Add(3 * time.Second)
	for a == nil && time.Now().Before(deadline) {
		a, err = c.NextAssignment("worker")
		if err != nil {
			t.Fatal(err)
		}
		if a == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if a == nil {
		t.Fatal("no workflow assignment")
	}
	before, err := store.ListAssignments("run")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err = c.Heartbeat("worker", WorkerHeartbeat{Active: []string{a.AssignmentID}, Leases: []state.AssignmentLease{{AssignmentID: a.AssignmentID, Generation: a.Generation, CoordinatorEpoch: a.CoordinatorEpoch}}}); err != nil {
		t.Fatal(err)
	}
	after, err := store.ListAssignments("run")
	if err != nil {
		t.Fatal(err)
	}
	if !after[0].LeaseUntil.After(before[0].LeaseUntil) {
		t.Fatal("heartbeat did not renew lease")
	}
	session := &contract.SessionHandle{ID: "opaque", Executor: "test", RunnerVersion: "runner-1", OwnerWorker: "worker", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour).UTC(), Recoverable: true}

	err = c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded", Outputs: map[string]any{"answer": "ok"}, Logs: "worker logs", Session: session, SessionID: "session", RemoteTask: &contract.RemoteTaskReference{ID: "task", URL: "https://example.test/task"}, Accounting: testaccounting.Tokens(9, 4, 2)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if result.Session == nil || *result.Session != *session || result.SessionID != "session" || result.RemoteTask == nil || result.RemoteTask.ID != "task" || result.RemoteTask.URL != "https://example.test/task" || result.Outputs["answer"] != "ok" || result.Logs != "worker logs" || result.Accounting == nil || testaccounting.TokenValue(result.Accounting, "input") != 9 || testaccounting.TokenValue(result.Accounting, "output") != 4 || testaccounting.TokenValue(result.Accounting, "cached") != 2 {
			t.Fatalf("lost completion: %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workflow did not receive completion")
	}
	raw, err := store.WorkerResult("attempt")
	if err != nil || len(raw) == 0 {
		t.Fatalf("result not durable: %s %v", raw, err)
	}
}

func TestReadinessSurvivesRegistrationAndHeartbeatBeforeLease(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	report := &contract.ProbeResult{Ready: false, Checks: []contract.ProbeCheck{{Name: contract.ProbeAuthentication, Passed: false, Message: "login required"}}}
	registration := WorkerRegistration{WorkerID: "worker", InstanceID: "process", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test", Contract: &contract.AdapterManifest{ContractVersion: "1", ProbeVersion: "1"}, Readiness: report}}}
	if _, err := c.Register(registration); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Heartbeat("worker", WorkerHeartbeat{InstanceID: "process"}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListWorkers(time.Time{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("workers: %v %v", listed, err)
	}
	got := listed[0].Capabilities[0].Readiness
	if got == nil || got.Ready || len(got.Checks) != 1 || got.Checks[0].Message != "login required" {
		t.Fatalf("lost readiness: %+v", got)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if a, err := c.NextAssignmentForInstance("worker", "process"); err != nil || a != nil {
		t.Fatalf("unready worker leased work: %v %v", a, err)
	}
	registration.Capabilities[0].Readiness = nil
	if _, err := c.Register(registration); err != nil {
		t.Fatal(err)
	}
	if a, err := c.NextAssignmentForInstance("worker", "process"); err != nil || a != nil {
		t.Fatalf("unverified worker leased work: %v %v", a, err)
	}
	registration.Capabilities[0].Readiness = &contract.ProbeResult{Ready: true, Checks: []contract.ProbeCheck{{Name: contract.ProbeAuthentication, Passed: true}}}
	if _, err := c.Register(registration); err != nil {
		t.Fatal(err)
	}
	if a, err := c.NextAssignmentForInstance("worker", "process"); err != nil || a == nil || a.Generation != 1 {
		t.Fatalf("ready worker could not claim first lease: %v %v", a, err)
	}
}
