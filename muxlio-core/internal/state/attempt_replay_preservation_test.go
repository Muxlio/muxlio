package state_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestRecordExecutionAttemptPreservesStartedAtOnReplay(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	replay := func(t *testing.T, s state.Store) {
		t.Helper()
		if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
			ID: "a", RunID: "r", ActivationID: "act", State: "running", StartedAt: start,
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
			ID: "a", RunID: "r", ActivationID: "act", State: "succeeded", FinishedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		attempts, err := s.ListExecutionAttempts("r", "act")
		if err != nil || len(attempts) != 1 {
			t.Fatalf("attempts=%v err=%v", attempts, err)
		}
		if !attempts[0].StartedAt.Equal(start) {
			t.Fatalf("StartedAt lost: got %v want %v", attempts[0].StartedAt, start)
		}
	}
	t.Run("memory", func(t *testing.T) {
		replay(t, state.New())
	})
	t.Run("sqlite", func(t *testing.T) {
		db, err := sqlite.Open(filepath.Join(t.TempDir(), "attempts.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		replay(t, db)
	})
}

func TestRecordExecutionAttemptPreservesAwaitReconciliationOnReplay(t *testing.T) {
	replay := func(t *testing.T, s state.Store) {
		t.Helper()
		if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
			ID: "a", RunID: "r", ActivationID: "act", State: "awaiting_reconciliation",
			AwaitReconciliationReason: "worker lost",
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
			ID: "a", RunID: "r", ActivationID: "act", State: "awaiting_reconciliation",
		}); err != nil {
			t.Fatal(err)
		}
		attempts, err := s.ListExecutionAttempts("r", "act")
		if err != nil || len(attempts) != 1 {
			t.Fatalf("attempts=%v err=%v", attempts, err)
		}
		if attempts[0].AwaitReconciliationReason != "worker lost" {
			t.Fatalf("reason lost: %q", attempts[0].AwaitReconciliationReason)
		}
	}
	t.Run("memory", func(t *testing.T) {
		replay(t, state.New())
	})
	t.Run("sqlite", func(t *testing.T) {
		db, err := sqlite.Open(filepath.Join(t.TempDir(), "attempts.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		replay(t, db)
	})
}
