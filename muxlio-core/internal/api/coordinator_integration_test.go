package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

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
	cost := 0.25
	err = c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded", Outputs: map[string]any{"answer": "ok"}, Logs: "worker logs", SessionID: "session", RemoteTask: &contract.RemoteTaskReference{ID: "task", URL: "https://example.test/task"}, Usage: &state.TokenUsage{Input: 9, Output: 4, Cached: 2}, Measurements: &contract.Measurements{CostUSD: &cost}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if result.SessionID != "session" || result.RemoteTask == nil || result.RemoteTask.ID != "task" || result.RemoteTask.URL != "https://example.test/task" || result.Outputs["answer"] != "ok" || result.Logs != "worker logs" || result.Usage == nil || result.Usage.Input != 9 || result.Usage.Output != 4 || result.Usage.Cached != 2 || result.Measurements == nil || result.Measurements.CostUSD == nil || *result.Measurements.CostUSD != cost {
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
