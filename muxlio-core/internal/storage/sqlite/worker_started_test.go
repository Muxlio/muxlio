package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestSQLitePreservesWorkerStartedAtAcrossReplay(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run", WorkflowName: "w", State: "running"}); err != nil {
		t.Fatal(err)
	}
	launch := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", RetryNumber: 1, Executor: "test",
		State: "running", StartedAt: launch.Add(-time.Second), WorkerStartedAt: &launch,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a1", RunID: "run", ActivationID: "act", RetryNumber: 1, Executor: "test",
		State: "succeeded", StartedAt: launch.Add(-time.Second), FinishedAt: launch.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListExecutionAttempts("run", "")
	if err != nil || len(attempts) != 1 || attempts[0].WorkerStartedAt == nil || !attempts[0].WorkerStartedAt.Equal(launch) {
		t.Fatalf("preserved launch missing: %+v err=%v want %v", attempts, err, launch)
	}
}
