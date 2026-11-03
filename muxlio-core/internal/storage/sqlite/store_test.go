package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestCreateAndGetRun(t *testing.T) {
	st := openTemp(t)
	run := &state.Run{ID: "run_a", WorkflowName: "batch", Input: map[string]any{"n": 1}}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun("run_a")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "created" || got.WorkflowName != "batch" {
		t.Fatalf("got %+v", got)
	}
	events, err := st.GetEvents("run_a", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != state.EventRunCreated {
		t.Fatalf("events = %+v", events)
	}
}

func TestAtomicFailureWritesEventAndState(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "run_b", WorkflowName: "batch"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunError("run_b", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetRun("run_b")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" || got.Error != "boom" {
		t.Fatalf("got %+v", got)
	}
	events, err := st.GetEvents("run_b", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Type != state.EventRunFailed {
		t.Fatalf("last event = %+v", events[len(events)-1])
	}
}

func TestRetryRunPreservesSucceededOutputsAndRequeuesIncomplete(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "run_retry", WorkflowName: "batch"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{ID: "act_done", RunID: "run_retry", NodeID: "done", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActivationOutputs("act_done", map[string]any{"value": "kept"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{ID: "act_failed", RunID: "run_retry", NodeID: "failed", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActivationError("act_failed", errors.New("retry me")); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunError("run_retry", errors.New("run failed")); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryRun("run_retry"); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun("run_retry")
	if err != nil || run.State != "running" {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	done, err := st.GetActivation("act_done")
	if err != nil || done.State != "succeeded" || done.Outputs["value"] != "kept" {
		t.Fatalf("succeeded activation = %+v, err = %v", done, err)
	}
	failed, err := st.GetActivation("act_failed")
	if err != nil || failed.State != "queued" || failed.Error != "" {
		t.Fatalf("retried activation = %+v, err = %v", failed, err)
	}
}

func TestReopenSeesCommittedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coordinator.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(&state.Run{ID: "run_c", WorkflowName: "batch", Input: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunState("run_c", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	got, err := st2.GetRun("run_c")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "succeeded" {
		t.Fatalf("state after reopen = %s", got.State)
	}
}

func TestCoordinatorRestartFencesAndHoldsUncertainDispatch(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "run_epoch", WorkflowName: "batch"}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueDispatch(state.Dispatch{AssignmentID: "as_epoch", RunID: "run_epoch", ActivationID: "act_epoch", AttemptID: "at_epoch", Executor: "agent/test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker_epoch", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ClaimDispatch("worker_epoch", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	epoch, err := st.StartCoordinator()
	if err != nil || epoch != 1 {
		t.Fatalf("StartCoordinator = %d, %v", epoch, err)
	}
	assignments, err := st.ListAssignments("run_epoch")
	if err != nil || len(assignments) != 1 || assignments[0].State != "uncertain" {
		t.Fatalf("assignments = %+v, %v", assignments, err)
	}
	d, generation, err := st.ClaimDispatch("worker_epoch", time.Now().Add(time.Hour))
	if err != nil || d != nil || generation != 0 {
		t.Fatalf("uncertain dispatch was retried = %+v generation=%d err=%v", d, generation, err)
	}
}

func TestCompleteWorkerAssignmentChecksIdentityAndLeaseAtomically(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "run_complete", WorkflowName: "batch"}); err != nil {
		t.Fatal(err)
	}
	lease := time.Now().Add(time.Minute)
	if _, err := st.ClaimAssignment(state.Assignment{RunID: "run_complete", ActivationID: "act_complete", WorkerID: "worker_complete", AssignmentID: "as_complete", AttemptID: "at_complete", LeaseUntil: lease}); err != nil {
		t.Fatal(err)
	}
	base := state.Assignment{RunID: "run_complete", ActivationID: "act_complete", WorkerID: "worker_complete", Generation: 1, AssignmentID: "as_complete", AttemptID: "at_complete"}
	if ok, err := st.CompleteWorkerAssignment(base, "succeeded", lease); err != nil || ok {
		t.Fatalf("completion at lease boundary = %v, %v", ok, err)
	}
	base.WorkerID = "other"
	if ok, err := st.CompleteWorkerAssignment(base, "succeeded", time.Now()); err != nil || ok {
		t.Fatalf("wrong worker completion = %v, %v", ok, err)
	}
	base.WorkerID = "worker_complete"
	if ok, err := st.CompleteWorkerAssignment(base, "failed", time.Now()); err != nil || !ok {
		t.Fatalf("valid completion = %v, %v", ok, err)
	}
	assignments, err := st.ListAssignments("run_complete")
	if err != nil || assignments[0].State != "failed" {
		t.Fatalf("terminal result state not retained: %+v, %v", assignments, err)
	}
}

func TestDuplicateWorkerCompletionIsIdempotent(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "duplicate-run"}); err != nil {
		t.Fatal(err)
	}
	a := state.Assignment{RunID: "duplicate-run", ActivationID: "activation", WorkerID: "worker", AssignmentID: "assignment", AttemptID: "attempt", LeaseUntil: time.Now().Add(time.Minute)}
	generation, err := st.ClaimAssignment(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Generation = generation
	result := []byte(`{"status":"succeeded","outputs":{"result":"ok"}}`)
	for i := 0; i < 2; i++ {
		if ok, err := st.CompleteWorkerAssignment(a, "succeeded", time.Now(), result); err != nil || !ok {
			t.Fatalf("completion %d = %v, %v", i, ok, err)
		}
	}
	if ok, err := st.CompleteWorkerAssignment(a, "succeeded", time.Now(), []byte(`{"different":true}`)); err != nil || ok {
		t.Fatalf("changed result = %v,%v", ok, err)
	}
	a.Generation++
	if ok, err := st.CompleteWorkerAssignment(a, "succeeded", time.Now(), result); err != nil || ok {
		t.Fatalf("changed generation = %v,%v", ok, err)
	}
}

func TestRecoveredEnqueueDoesNotRequeueClaimedAttempt(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "enqueue-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker", Capacity: 2, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	request := state.Dispatch{AssignmentID: "assignment", RunID: "enqueue-run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}
	if err := st.EnqueueDispatch(request); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || claimed == nil {
		t.Fatalf("first claim: %v,%v", claimed, err)
	}
	if err := st.EnqueueDispatch(request); err != nil {
		t.Fatal(err)
	}
	claimed, _, err = st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || claimed != nil {
		t.Fatalf("duplicate claim: %v,%v", claimed, err)
	}
}

func TestReconcileUncertainDispatchRequiresTerminalAssignment(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "reconcile-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	request := state.Dispatch{AssignmentID: "assignment", RunID: "reconcile-run", ActivationID: "activation", AttemptID: "attempt", Executor: "test", Request: []byte(`{}`)}
	if err := st.EnqueueDispatch(request); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ClaimDispatch("worker", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileUncertainDispatch("activation"); err == nil {
		t.Fatal("reconciled an active assignment")
	}
	if _, err := st.StartCoordinator(); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT generation FROM assignments WHERE activation_id='activation'`).Scan(new(int64)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE assignments SET state='completed' WHERE activation_id='activation'`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileUncertainDispatch("activation"); err != nil {
		t.Fatal(err)
	}
	claimed, _, err := st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || claimed == nil {
		t.Fatalf("reconciled dispatch not claimable: %v, %v", claimed, err)
	}
}

func TestListObservabilityState(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "run_obs", WorkflowName: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{ID: "act_obs", RunID: "run_obs", NodeID: "task", ScopePath: []string{"loop[0]"}, State: "running"}); err != nil {
		t.Fatal(err)
	}
	acts, err := st.ListActivations("run_obs")
	if err != nil || len(acts) != 1 || acts[0].ScopePath[0] != "loop[0]" {
		t.Fatalf("activations = %+v, err=%v", acts, err)
	}
	if err := st.AppendLogChunk(state.LogChunk{RunID: "run_obs", ActivationID: "act_obs", Stream: "stderr", Seq: 1, Data: []byte("oops")}); err != nil {
		t.Fatal(err)
	}
	logs, err := st.GetLogChunks("run_obs", "act_obs")
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %+v, err=%v", logs, err)
	}
	if _, err := st.ClaimAssignment(state.Assignment{RunID: "run_obs", ActivationID: "act_obs", WorkerID: "worker", State: "active"}); err != nil {
		t.Fatal(err)
	}
	assignments, err := st.ListAssignments("run_obs")
	if err != nil || len(assignments) != 1 || assignments[0].WorkerID != "worker" {
		t.Fatalf("assignments = %+v, err=%v", assignments, err)
	}
}

func TestExecutionAttemptsArePersistedAndEmitEvents(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "attempt-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{ID: "attempt-act", RunID: "attempt-run", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	a := state.ExecutionAttempt{ID: "attempt-act-attempt-1", RunID: "attempt-run", ActivationID: "attempt-act", RetryNumber: 1, Executor: "test/echo", State: "failed", StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(), Failure: "boom"}
	if err := st.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListExecutionAttempts("attempt-run", "attempt-act")
	if err != nil || len(got) != 1 || got[0].Failure != "boom" || got[0].RetryNumber != 1 {
		t.Fatalf("attempts=%+v err=%v", got, err)
	}
	events, err := st.GetEvents("attempt-run", 0, 0)
	if err != nil || events[len(events)-1].Type != state.EventExecutionAttempt {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestSQLiteExecutionAttemptUsage(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "usage-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{ID: "usage-act", RunID: "usage-run", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}

	// Reported usage persists and round-trips.
	reported := &state.TokenUsage{Input: 5000, Output: 3000, Cached: 1500}
	if err := st.RecordExecutionAttempt(state.ExecutionAttempt{ID: "usage-act-1", RunID: "usage-run", ActivationID: "usage-act", RetryNumber: 1, Executor: "agent/x", State: "succeeded", StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(), Usage: reported}); err != nil {
		t.Fatal(err)
	}

	// Missing usage stays nil (unavailable), not zero.
	if err := st.RecordExecutionAttempt(state.ExecutionAttempt{ID: "usage-act-2", RunID: "usage-run", ActivationID: "usage-act", RetryNumber: 2, Executor: "agent/x", State: "succeeded", StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// Reported zero is distinct from nil.
	if err := st.RecordExecutionAttempt(state.ExecutionAttempt{ID: "usage-act-3", RunID: "usage-run", ActivationID: "usage-act", RetryNumber: 3, Executor: "agent/x", State: "succeeded", StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(), Usage: &state.TokenUsage{}}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListExecutionAttempts("usage-run", "usage-act")
	if err != nil || len(got) != 3 {
		t.Fatalf("expected 3 attempts, got=%+v err=%v", got, err)
	}

	byID := make(map[string]state.ExecutionAttempt, len(got))
	for _, a := range got {
		byID[a.ID] = a
	}

	a1 := byID["usage-act-1"]
	if a1.Usage == nil || a1.Usage.Input != 5000 || a1.Usage.Output != 3000 || a1.Usage.Cached != 1500 {
		t.Fatalf("reported usage mismatch: %+v", a1.Usage)
	}

	a2 := byID["usage-act-2"]
	if a2.Usage != nil {
		t.Fatalf("missing usage should be nil, not zero: %+v", a2.Usage)
	}

	a3 := byID["usage-act-3"]
	if a3.Usage == nil {
		t.Fatal("reported zero usage should be non-nil")
	}
	if a3.Usage.Input != 0 || a3.Usage.Output != 0 || a3.Usage.Cached != 0 {
		t.Fatalf("reported zero should have zero fields: %+v", a3.Usage)
	}
}

func TestSQLiteExecutionAttemptUsagePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(&state.Run{ID: "persist-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateActivation(&state.Activation{ID: "persist-act", RunID: "persist-run", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordExecutionAttempt(state.ExecutionAttempt{ID: "persist-act-1", RunID: "persist-run", ActivationID: "persist-act", RetryNumber: 1, Executor: "agent/x", State: "succeeded", StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(), Usage: &state.TokenUsage{Input: 42, Output: 17, Cached: 5}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, err := st2.ListExecutionAttempts("persist-run", "persist-act")
	if err != nil || len(got) != 1 || got[0].Usage == nil {
		t.Fatalf("usage not persisted across reopen: %+v err=%v", got, err)
	}
	if got[0].Usage.Input != 42 || got[0].Usage.Output != 17 || got[0].Usage.Cached != 5 {
		t.Fatalf("usage mismatch after reopen: %+v", got[0].Usage)
	}
}

func TestClaimDispatchVersionMismatch(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "version-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test", Version: "2.0"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"executor":"agent/test","executorVersion":"1.0"}`)
	if err := st.EnqueueDispatch(state.Dispatch{AssignmentID: "as-1", RunID: "version-run", ActivationID: "act-1", AttemptID: "at-1", Executor: "agent/test", Request: request}); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || got != nil {
		t.Fatalf("version mismatch claim = %+v, %v", got, err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test", Version: "1.0"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, _, err = st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || got == nil || got.AssignmentID != "as-1" {
		t.Fatalf("matching version claim = %+v, %v", got, err)
	}

	// An unpinned request is compatible regardless of the advertised version.
	if _, err := st.CompleteAssignment("act-1", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueDispatch(state.Dispatch{AssignmentID: "as-2", RunID: "version-run", ActivationID: "act-2", AttemptID: "at-2", Executor: "agent/test", Request: []byte(`{"executor":"agent/test"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test", Version: "99.0"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, _, err = st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || got == nil || got.AssignmentID != "as-2" {
		t.Fatalf("unversioned request claim = %+v, %v", got, err)
	}
}

func TestClaimDispatchLabelMatch(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "label-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test"}}, Labels: map[string]string{"region": "eu-west"}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"executor":"agent/test","placementLabels":{"region":"eu-west"}}`)
	if err := st.EnqueueDispatch(state.Dispatch{AssignmentID: "as-1", RunID: "label-run", ActivationID: "act-1", AttemptID: "at-1", Executor: "agent/test", Request: request}); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || got == nil || got.AssignmentID != "as-1" {
		t.Fatalf("label match claim = %+v, %v", got, err)
	}
}

func TestClaimDispatchLabelMismatch(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "label-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test"}}, Labels: map[string]string{"region": "eu-west"}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"executor":"agent/test","placementLabels":{"region":"us-east"}}`)
	if err := st.EnqueueDispatch(state.Dispatch{AssignmentID: "as-1", RunID: "label-run", ActivationID: "act-1", AttemptID: "at-1", Executor: "agent/test", Request: request}); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.ClaimDispatch("worker", time.Now().Add(time.Minute))
	if err != nil || got != nil {
		t.Fatalf("label mismatch claim = %+v, %v", got, err)
	}
}

func TestDispatchHonorsWorkerCapacityAndCapability(t *testing.T) {
	st := openTemp(t)
	if err := st.CreateRun(&state.Run{ID: "dispatch-run"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker-a", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterWorker(state.Worker{ID: "worker-other", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/other"}}, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []state.Dispatch{
		{AssignmentID: "assignment-1", RunID: "dispatch-run", ActivationID: "activation-1", AttemptID: "attempt-1", Executor: "agent/test", Request: []byte(`{"executor":"agent/test"}`)},
		{AssignmentID: "assignment-2", RunID: "dispatch-run", ActivationID: "activation-2", AttemptID: "attempt-2", Executor: "agent/test", Request: []byte(`{"executor":"agent/test"}`)},
	} {
		if err := st.EnqueueDispatch(d); err != nil {
			t.Fatal(err)
		}
	}
	got, generation, err := st.ClaimDispatch("worker-other", time.Now().Add(time.Minute))
	if err != nil || got != nil {
		t.Fatalf("nonmatching capability claim = %+v, %v", got, err)
	}
	got, generation, err = st.ClaimDispatch("worker-a", time.Now().Add(time.Minute))
	if err != nil || got == nil || got.AssignmentID != "assignment-1" || generation != 1 {
		t.Fatalf("first claim = %+v generation=%d err=%v", got, generation, err)
	}
	got, _, err = st.ClaimDispatch("worker-a", time.Now().Add(time.Minute))
	if err != nil || got != nil {
		t.Fatalf("claim above capacity = %+v, %v", got, err)
	}
}

func TestOutboxLifecycle(t *testing.T) {
	st := openTemp(t)
	// Initially empty
	unpub, err := st.ListUnpublishedOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpub) != 0 {
		t.Fatalf("expected 0 unpublished, got %d", len(unpub))
	}

	// Enqueue 3 records
	if err := st.EnqueueOutbox("msg-1", "assignment.created", map[string]any{"activationId": "act-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueOutbox("msg-2", "run.created", map[string]any{"runId": "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueOutbox("msg-3", "worker.notified", map[string]any{"workerId": "worker-1"}); err != nil {
		t.Fatal(err)
	}

	// List with limit
	unpub, err = st.ListUnpublishedOutbox(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpub) != 2 {
		t.Fatalf("expected 2 records with limit 2, got %d", len(unpub))
	}
	if unpub[0].ID != "msg-1" || unpub[1].ID != "msg-2" {
		t.Fatalf("unexpected order: %+v", unpub)
	}

	// List all
	unpub, err = st.ListUnpublishedOutbox(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpub) != 3 {
		t.Fatalf("expected 3 records, got %d", len(unpub))
	}

	// Mark msg-1 published
	now := time.Now()
	if err := st.MarkOutboxPublished("msg-1", now); err != nil {
		t.Fatal(err)
	}

	// Now only msg-2 and msg-3 are unpublished
	unpub, err = st.ListUnpublishedOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpub) != 2 {
		t.Fatalf("expected 2 unpublished, got %d", len(unpub))
	}
	if unpub[0].ID != "msg-2" || unpub[1].ID != "msg-3" {
		t.Fatalf("unexpected unpublished: %+v", unpub)
	}

	// Error when marking nonexistent record
	if err := st.MarkOutboxPublished("msg-nonexistent", now); err == nil {
		t.Fatal("expected error marking nonexistent record")
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
