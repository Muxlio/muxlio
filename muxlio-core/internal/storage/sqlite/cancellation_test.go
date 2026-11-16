package sqlite

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestQueueAndExecutionTimeoutsHaveIndependentWindows(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "timeouts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	queueDeadline := time.Now().Add(time.Second)
	seconds := 0.01
	raw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", QueueDeadline: &queueDeadline, TimeoutSeconds: &seconds})
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "a", ActivationID: "act", RunID: "run", AttemptID: "attempt", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	d, g, err := s.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || d == nil {
		t.Fatalf("dispatch=%+v err=%v", d, err)
	}
	var req workers.ExecuteRequest
	if err := json.Unmarshal(d.Request, &req); err != nil {
		t.Fatal(err)
	}
	if req.ExecutionDeadline == nil {
		t.Fatal("missing persisted execution deadline")
	}
	if expired, err := s.ExpireQueuedAttempt("attempt", queueDeadline.Add(time.Second)); err != nil || expired {
		t.Fatalf("queue timeout affected admitted work: %v %v", expired, err)
	}
	// Completion observed at the deadline loses deterministically, even before
	// a coordinator timeout sweep has run.
	a := state.Assignment{RunID: "run", ActivationID: "act", WorkerID: "worker", AssignmentID: "a", AttemptID: "attempt", Generation: g}
	ok, err := s.CompleteWorkerAssignment(a, "succeeded", *req.ExecutionDeadline, []byte(`{"outputs":{"value":"late"}}`))
	if err != nil || ok {
		t.Fatalf("late completion accepted: %v %v", ok, err)
	}
	if raw, _ := s.WorkerResult("attempt"); len(raw) != 0 {
		t.Fatalf("late result committed: %s", raw)
	}
}
func TestCancelledScopeCannotBeResurrected(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"parent", "child"} {
		if err := s.CreateActivation(&state.Activation{ID: id, NodeID: id, RunID: "run", State: "queued"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CancelRunWork("run"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivationState("child", "running"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivationOutputs("child", map[string]any{"late": true}); err != nil {
		t.Fatal(err)
	}
	act, _ := s.GetActivation("child")
	if act.State != "cancelled" || act.Outputs != nil {
		t.Fatalf("act=%+v", act)
	}
}

func TestExpiredQueueCannotBeClaimedBeforeTimeoutSweep(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(-time.Second)
	raw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", QueueDeadline: &deadline})
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "a", ActivationID: "act", RunID: "run", AttemptID: "attempt", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	d, _, err := s.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || d != nil {
		t.Fatalf("expired queued work dispatched: %+v %v", d, err)
	}
	if expired, err := s.ExpireQueuedAttempt("attempt", time.Now()); err != nil || !expired {
		t.Fatalf("expiry=%v err=%v", expired, err)
	}
}

func TestLostWorkerCannotEscapeTimeoutOrCancellation(t *testing.T) {
	for _, effect := range []string{"retryable", "none"} {
		for _, action := range []string{"timeout", "cancel"} {
			t.Run(effect+"/"+action, func(t *testing.T) {
				s, err := Open(filepath.Join(t.TempDir(), "lost.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
					t.Fatal(err)
				}
				if err := s.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", Effect: effect})
				if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "a", ActivationID: "act", RunID: "run", AttemptID: "attempt", Executor: "test", Request: raw}); err != nil {
					t.Fatal(err)
				}
				d, generation, err := s.ClaimDispatch("worker", time.Now().Add(time.Minute))
				if err != nil || d == nil {
					t.Fatalf("claim=%+v err=%v", d, err)
				}
				// Persist an expired deadline and simulate a lost worker before the sweep.
				past := time.Now().Add(-time.Second)
				raw, _ = json.Marshal(workers.ExecuteRequest{Executor: "test", Effect: effect, ExecutionDeadline: &past})
				if _, err := s.db.Exec(`UPDATE dispatches SET request_json=? WHERE attempt_id='attempt'`, string(raw)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE assignments SET lease_until=? WHERE attempt_id='attempt'`, rfc3339(past)); err != nil {
					t.Fatal(err)
				}
				if err := s.RecoverWorkerLeases(); err != nil {
					t.Fatal(err)
				}
				var dispatchState string
				if err := s.db.QueryRow(`SELECT state FROM dispatches WHERE attempt_id='attempt'`).Scan(&dispatchState); err != nil {
					t.Fatal(err)
				}
				if dispatchState != "uncertain" {
					t.Fatalf("expired work requeued: %s", dispatchState)
				}
				if action == "timeout" {
					if expired, err := s.ExpireExecutingAttempt("attempt", time.Now()); err != nil || !expired {
						t.Fatalf("expired=%v err=%v", expired, err)
					}
				} else {
					if err := s.CancelRunWork("run"); err != nil {
						t.Fatal(err)
					}
				}
				assignments, err := s.ListAssignments("run")
				if err != nil || len(assignments) != 1 || assignments[0].State != "cancelled" || assignments[0].Generation <= generation {
					t.Fatalf("assignments=%+v err=%v", assignments, err)
				}
				if err := s.db.QueryRow(`SELECT state FROM dispatches WHERE attempt_id='attempt'`).Scan(&dispatchState); err != nil {
					t.Fatal(err)
				}
				if dispatchState != "cancelled" {
					t.Fatalf("dispatch=%s", dispatchState)
				}
				d, _, err = s.ClaimDispatch("worker", time.Now().Add(time.Minute))
				if err != nil || d != nil {
					t.Fatalf("cancelled work dispatched: %+v %v", d, err)
				}
			})
		}
	}
}

func TestQueueTimeoutAllowsFreshFallbackDispatch(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "act", RunID: "run", NodeID: "node", State: "running"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(-time.Second)
	raw, _ := json.Marshal(workers.ExecuteRequest{QueueDeadline: &deadline})
	d := state.Dispatch{AssignmentID: "first", RunID: "run", ActivationID: "act", AttemptID: "first", Executor: "primary", Request: raw}
	if err := s.EnqueueDispatch(d); err != nil {
		t.Fatal(err)
	}
	acts, err := s.ListActivations("run")
	if err != nil || len(acts) != 1 || acts[0].State != "queued" {
		t.Fatalf("queued activations=%+v err=%v", acts, err)
	}
	if expired, err := s.ExpireQueuedAttempt("first", time.Now()); err != nil || !expired {
		t.Fatalf("expired=%v err=%v", expired, err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "second", RunID: "run", ActivationID: "act", Executor: "fallback", State: "queued", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	d.AssignmentID = "second"
	d.AttemptID = "second"
	d.Executor = "fallback"
	d.Request = []byte(`{}`)
	if err := s.EnqueueDispatch(d); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "fallback"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := s.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || claimed == nil || claimed.AttemptID != "second" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	acts, err = s.ListActivations("run")
	if err != nil || len(acts) != 1 || acts[0].State != "running" {
		t.Fatalf("running activations=%+v err=%v", acts, err)
	}
	attempts, err := s.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || attempts[0].State != "running" {
		t.Fatalf("claimed state=%+v err=%v", attempts, err)
	}
}

func TestRecoveredQueueWithoutDeadlineUsesOriginalQueuedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-queue.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	queuedAt := time.Now().Add(-4 * time.Minute)
	raw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", QueuedAt: &queuedAt})
	dispatch := state.Dispatch{AssignmentID: "attempt", RunID: "run", ActivationID: "act", AttemptID: "attempt", Executor: "test", Request: raw}
	if err := s.EnqueueDispatch(dispatch); err != nil {
		t.Fatal(err)
	}
	// Model a dispatch persisted by a release with no default queue deadline.
	if _, err := s.db.Exec(`UPDATE dispatches SET request_json=? WHERE attempt_id='attempt'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// The new coordinator supplies a bounded request when reattaching. The
	// previously persisted timestamp remains authoritative for this attempt.
	newDeadline := time.Now().Add(3 * time.Minute)
	dispatch.Request, _ = json.Marshal(workers.ExecuteRequest{Executor: "test", QueueDeadline: &newDeadline})
	if err := s.EnqueueDispatch(dispatch); err != nil {
		t.Fatal(err)
	}
	persisted, err := s.AssignmentRequest("attempt")
	if err != nil {
		t.Fatal(err)
	}
	var req workers.ExecuteRequest
	if err := json.Unmarshal(persisted, &req); err != nil {
		t.Fatal(err)
	}
	if req.QueueDeadline == nil || !req.QueueDeadline.Equal(queuedAt.Add(3*time.Minute)) {
		t.Fatalf("recovery extended the queue wait: %+v", req.QueueDeadline)
	}
	if err := s.EnqueueDispatch(dispatch); err != nil {
		t.Fatal(err)
	}
	again, err := s.AssignmentRequest("attempt")
	if err != nil || string(again) != string(persisted) {
		t.Fatalf("reattachment reset deadline: %s %v", again, err)
	}
	if err := s.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := s.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || claimed != nil {
		t.Fatalf("expired queued work reached worker: %+v %v", claimed, err)
	}
	if expired, err := s.ExpireQueuedAttempt("attempt", time.Now()); err != nil || !expired {
		t.Fatalf("legacy queue did not expire: %v %v", expired, err)
	}
}

func TestAlreadyCancelledDispatchIsNotATimeout(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	deadline := time.Now().Add(time.Minute)
	raw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", QueueDeadline: &past})
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "queued", ActivationID: "queued", AttemptID: "queued", RunID: "run", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelAttemptWork("queued"); err != nil {
		t.Fatal(err)
	}
	if expired, err := s.ExpireQueuedAttempt("queued", time.Now()); err != nil || expired {
		t.Fatalf("cancelled queue item looked like a timeout: %v %v", expired, err)
	}
	runningRaw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", ExecutionDeadline: &deadline})
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "running", ActivationID: "running", AttemptID: "running", RunID: "run", Executor: "test", Request: runningRaw}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := s.ClaimDispatch("worker", time.Now().Add(time.Minute)); err != nil || claimed == nil || claimed.AttemptID != "running" {
		t.Fatalf("claim=%+v %v", claimed, err)
	}
	if err := s.CancelAttemptWork("running"); err != nil {
		t.Fatal(err)
	}
	if expired, err := s.ExpireExecutingAttempt("running", deadline.Add(time.Second)); err != nil || !expired {
		t.Fatalf("deadline-rejected execution did not unblock the waiter: %v %v", expired, err)
	}
	future := time.Now().Add(time.Minute)
	futureRaw, _ := json.Marshal(workers.ExecuteRequest{Executor: "test", ExecutionDeadline: &future})
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "early", ActivationID: "early", AttemptID: "early", RunID: "run", Executor: "test", Request: futureRaw}); err != nil {
		t.Fatal(err)
	}
	if claimed, _, err := s.ClaimDispatch("worker", time.Now().Add(time.Minute)); err != nil || claimed == nil || claimed.AttemptID != "early" {
		t.Fatalf("claim=%+v %v", claimed, err)
	}
	if err := s.CancelAttemptWork("early"); err != nil {
		t.Fatal(err)
	}
	if expired, err := s.ExpireExecutingAttempt("early", time.Now()); err != nil || expired {
		t.Fatalf("cancel before the execution deadline looked like a timeout: %v %v", expired, err)
	}
}
