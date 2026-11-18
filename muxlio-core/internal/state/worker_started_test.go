package state_test

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestRecordExecutionAttemptPreservesWorkerStartedAtOnReplay(t *testing.T) {
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "w", State: "running"}); err != nil {
		t.Fatal(err)
	}
	launch := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", RetryNumber: 1, Executor: "test",
		State: "running", StartedAt: launch.Add(-time.Second), WorkerStartedAt: &launch,
	}
	if err := store.RecordExecutionAttempt(first); err != nil {
		t.Fatal(err)
	}
	replay := state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", RetryNumber: 1, Executor: "test",
		State: "succeeded", StartedAt: launch.Add(-time.Second), FinishedAt: launch.Add(time.Second),
	}
	if err := store.RecordExecutionAttempt(replay); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	if attempts[0].WorkerStartedAt == nil || !attempts[0].WorkerStartedAt.Equal(launch) {
		t.Fatalf("replay cleared launch: got %v want %v", attempts[0].WorkerStartedAt, launch)
	}
}

func TestMissingWorkerStartedAtStaysUnavailable(t *testing.T) {
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "w", State: "running"}); err != nil {
		t.Fatal(err)
	}
	a := state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", RetryNumber: 1, Executor: "test",
		State: "failed", StartedAt: time.Now(), FinishedAt: time.Now(), Failure: "queue timeout",
	}
	if err := store.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || attempts[0].WorkerStartedAt != nil {
		t.Fatalf("expected unavailable workerStartedAt: %+v err=%v", attempts, err)
	}
}
