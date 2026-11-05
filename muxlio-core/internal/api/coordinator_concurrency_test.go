package api

import (
	"context"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/workers"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestCoordinatorCapacityReductionPreservesAdmittedWork(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", InstanceID: "process", Capacity: 4, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("attempt-%d", i)
		if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: id, RunID: "run", ActivationID: id, AttemptID: id, Executor: "test", Request: []byte(`{"executor":"test"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	var admitted []*WorkerAssignment
	for i := 0; i < 4; i++ {
		a, err := c.NextAssignmentForInstance("worker", "process")
		if err != nil || a == nil {
			t.Fatalf("claim: %v, %v", a, err)
		}
		admitted = append(admitted, a)
	}
	if a, err := c.NextAssignmentForInstance("worker", "process"); err != nil || a != nil {
		t.Fatalf("over capacity: %v, %v", a, err)
	}
	limit := 1
	ids := []string{}
	for _, a := range admitted {
		ids = append(ids, a.AssignmentID)
	}
	if _, err := c.Heartbeat("worker", WorkerHeartbeat{InstanceID: "process", Active: ids, Capacity: &limit}); err != nil {
		t.Fatal(err)
	}
	for i, a := range admitted {
		if next, err := c.NextAssignmentForInstance("worker", "process"); err != nil || next != nil {
			t.Fatalf("admitted after reduction at %d: %v, %v", i, next, err)
		}
		if err := c.Complete("worker", WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		if result, err := store.WorkerResult(a.AttemptID); err != nil || result == nil {
			t.Fatalf("admitted work lost: %s %v", result, err)
		}
	}
	if a, err := c.NextAssignmentForInstance("worker", "process"); err != nil || a == nil {
		t.Fatalf("capacity never reopened: %v, %v", a, err)
	}
}

func TestReplacementWorkerFencesOldProcessSuccess(t *testing.T) {
	for _, replacement := range []string{"replacement-process", ""} {
		t.Run("replacement="+replacement, func(t *testing.T) {
			store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			c := NewCoordinator(store)
			registration := WorkerRegistration{WorkerID: "worker", InstanceID: "old-process", Capacity: 4, Capabilities: []WorkerCapability{{Name: "test"}}}
			if _, err := c.Register(registration); err != nil {
				t.Fatal(err)
			}
			if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "old", RunID: "run", ActivationID: "activation", AttemptID: "old", Executor: "test", Request: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			old, err := c.NextAssignmentForInstance("worker", registration.InstanceID)
			if err != nil || old == nil {
				t.Fatalf("claim: %v, %v", old, err)
			}
			// A retry by the same process keeps its admitted work.
			if _, err := c.Register(registration); err != nil {
				t.Fatal(err)
			}
			known, err := store.LookupWorkerAssignment("worker", old.AssignmentID)
			if err != nil || known == nil || known.State != "active" {
				t.Fatalf("same process lost lease: %v, %v", known, err)
			}
			// Simulate a crash and immediate restart before the old lease expires.
			registration.InstanceID = replacement
			if _, err := c.Register(registration); err != nil {
				t.Fatal(err)
			}
			completion := WorkerCompletion{AssignmentID: old.AssignmentID, RunID: old.RunID, ActivationID: old.ActivationID, AttemptID: old.AttemptID, CoordinatorEpoch: old.CoordinatorEpoch, Generation: old.Generation, State: "succeeded", Outputs: map[string]any{"stale": true}}
			_ = c.Complete("worker", completion) // A diagnostic acknowledgement is allowed.
			if result, err := store.WorkerResult("old"); err != nil || result != nil {
				t.Fatalf("stale success became a result: %s, %v", result, err)
			}
			known, err = store.LookupWorkerAssignment("worker", old.AssignmentID)
			if err != nil || known == nil || known.State != "uncertain" || known.Generation <= old.Generation {
				t.Fatalf("old lease not fenced: %v, %v", known, err)
			}
			// Heartbeats cannot resurrect the old generation, nor replay uncertain work.
			if _, err := c.Heartbeat("worker", WorkerHeartbeat{InstanceID: registration.InstanceID, Active: []string{"old"}}); err != nil {
				t.Fatal(err)
			}
			if a, err := c.NextAssignmentForInstance("worker", registration.InstanceID); err != nil || a != nil {
				t.Fatalf("uncertain work replayed: %v, %v", a, err)
			}
		})
	}
}

// A replaced process must neither overwrite the new capacity nor consume its
// assignments. Exercise the HTTP poll path as well as the heartbeat backend.
func TestReplacementWorkerRejectsOldAdmissionsAndHeartbeats(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	reg := WorkerRegistration{WorkerID: "worker", InstanceID: "old", Capacity: 4, Capabilities: []WorkerCapability{{Name: "test"}}}
	if _, err := c.Register(reg); err != nil {
		t.Fatal(err)
	}
	reg.InstanceID, reg.Capacity = "new", 1
	if _, err := c.Register(reg); err != nil {
		t.Fatal(err)
	}
	limit := 4
	for _, identity := range []string{"old", ""} {
		if _, err := c.Heartbeat("worker", WorkerHeartbeat{InstanceID: identity, Capacity: &limit}); err == nil {
			t.Fatalf("accepted heartbeat from %q", identity)
		}
	}
	workers, err := store.ListWorkers(time.Time{})
	if err != nil || len(workers) != 1 || workers[0].Capacity != 1 || workers[0].InstanceID != "new" {
		t.Fatalf("stale heartbeat changed registration: %+v, %v", workers, err)
	}
	if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: "next", RunID: "run", ActivationID: "next", AttemptID: "next", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	handler := NewWorkerProtocolHandler(c)
	for _, identity := range []string{"old", ""} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/workers/worker/assignments?instanceId="+identity, nil))
		if response.Code != http.StatusForbidden {
			t.Fatalf("stale poll %q: %d %s", identity, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/workers/worker/assignments?instanceId=new", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("replacement poll: %d %s", response.Code, response.Body.String())
	}
}

// Pure work recovers on the coordinator's execution loop even without another
// worker poll triggering expiry cleanup. A diagnostic must never release unsafe
// work; the corresponding unsafe recovery cases live in the storage suite.
func TestCoordinatorPureExecutionRecoversAfterLeaseLoss(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	c.lease = 20 * time.Millisecond
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker", Capacity: 1, Capabilities: []WorkerCapability{{Name: "test"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type outcome struct {
		result workers.ExecuteResult
		err    error
	}
	results := make(chan outcome, 1)
	go func() {
		r, e := c.Execute(workers.ExecuteRequest{Context: ctx, RunID: "run", ActivationID: "act", AttemptID: "attempt", Executor: "test", Effect: "none"})
		results <- outcome{r, e}
	}()
	claim := func() *WorkerAssignment {
		t.Helper()
		for ctx.Err() == nil {
			a, err := c.NextAssignment("worker")
			if err != nil {
				t.Fatal(err)
			}
			if a != nil {
				return a
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("assignment did not become eligible")
		return nil
	}
	old := claim()
	// No polling during this interval: Execute's recovery tick must expire it.
	for ctx.Err() == nil {
		known, err := store.LookupWorkerAssignment("worker", old.AssignmentID)
		if err != nil {
			t.Fatal(err)
		}
		if known.State == "cancelled" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("pure lease was not recovered by execution loop")
	}
	fresh := claim()
	if fresh.Generation <= old.Generation {
		t.Fatal("replay did not advance fencing generation")
	}
	completion := func(a *WorkerAssignment, value string) WorkerCompletion {
		return WorkerCompletion{AssignmentID: a.AssignmentID, AttemptID: a.AttemptID, ActivationID: a.ActivationID, RunID: a.RunID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded", Outputs: map[string]any{"value": value}}
	}
	if err := c.Complete("worker", completion(fresh, "fresh")); err != nil {
		t.Fatal(err)
	}
	_ = c.Complete("worker", completion(old, "stale"))
	select {
	case o := <-results:
		if o.err != nil || o.result.Outputs["value"] != "fresh" {
			t.Fatalf("recovery result: %+v %v", o.result, o.err)
		}
	case <-ctx.Done():
		t.Fatal("pure result not delivered")
	}
}
