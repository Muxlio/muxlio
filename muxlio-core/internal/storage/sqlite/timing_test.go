package sqlite

import (
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-core/internal/state"
	"path/filepath"
	"testing"
)

func TestTimingPersistsReplayAndRunRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timing.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateActivation(&state.Activation{ID: "a", RunID: "r", NodeID: "task", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	a := state.ExecutionAttempt{ID: "a-attempt-1", RunID: "r", ActivationID: "a", RetryNumber: 1, State: "failed", ExecutionDurationMS: &zero}
	if err = s.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	a.ExecutionDurationMS = nil
	if err = s.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "legacy", RunID: "r", ActivationID: "old", RetryNumber: 1, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRunError("r", errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	if err = s.RetryRun("r"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	act, err := s.GetActivation("a")
	if err != nil || act.AttemptBase != 1 {
		t.Fatalf("act=%+v err=%v", act, err)
	}
	attempts, err := s.ListExecutionAttempts("r", "")
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	for _, a := range attempts {
		if a.ID == "legacy" {
			if a.ExecutionDurationMS != nil {
				t.Fatal("legacy timing invented")
			}
		} else if a.ExecutionDurationMS == nil || *a.ExecutionDurationMS != 0 {
			t.Fatal("measured zero lost on replay/reopen")
		}
	}
}

func TestWorkerTimingReceiptsSurviveCancellationRecoveryAndReplay(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	a := state.Assignment{RunID: "r", ActivationID: "act", WorkerID: "worker", AssignmentID: "assignment", AttemptID: "attempt"}
	gen, err := s.ClaimAssignment(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Generation = gen
	ms := int64(40)
	attempt := state.ExecutionAttempt{ID: "attempt", RunID: "r", ActivationID: "act", RetryNumber: 1, State: "cancelled"}
	if err = s.RecordExecutionAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.RecordWorkerExecutionDuration(a, ms); err != nil {
			t.Fatal(err)
		}
	}
	// A recovered assignment can execute again; both actual worker spans count.
	gen, err = s.ClaimAssignment(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Generation = gen
	if err = s.RecordWorkerExecutionDuration(a, 80); err != nil {
		t.Fatal(err)
	}
	attempt.State = "succeeded"
	attempt.ExecutionDurationMS = &ms
	if err = s.RecordExecutionAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListExecutionAttempts("r", "")
	if err != nil || len(got) != 1 || got[0].ExecutionDurationMS == nil || *got[0].ExecutionDurationMS != 120 {
		t.Fatalf("receipts lost or counted twice: %+v err=%v", got, err)
	}
}

func TestLegacyTimingSchemaMigratesWithoutInventingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "legacy", RunID: "r", ActivationID: "a", RetryNumber: 1, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`ALTER TABLE execution_attempts DROP COLUMN execution_duration_ms`, `ALTER TABLE activations DROP COLUMN attempt_base`, `DROP TABLE worker_execution_timings`} {
		if _, err = s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	attempts, err := s.ListExecutionAttempts("r", "")
	if err != nil || len(attempts) != 1 || attempts[0].ExecutionDurationMS != nil {
		t.Fatalf("migration invented timing: %+v err=%v", attempts, err)
	}
}

func TestUnknownWorkerTimingCannotEraseExistingMeasurement(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	ms := int64(123)
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "r", ActivationID: "act", State: "succeeded", ExecutionDurationMS: &ms}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordWorkerExecutionDuration(state.Assignment{RunID: "r", ActivationID: "act", AttemptID: "attempt", AssignmentID: "unknown", WorkerID: "other"}, 999); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListExecutionAttempts("r", "")
	if err != nil || len(got) != 1 || got[0].ExecutionDurationMS == nil || *got[0].ExecutionDurationMS != 123 {
		t.Fatalf("unmatched receipt changed timing: %+v err=%v", got, err)
	}
}

func TestLateWorkerTimingEmitsEventOnce(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	a := state.Assignment{RunID: "r", ActivationID: "act", AttemptID: "attempt", AssignmentID: "assignment", WorkerID: "worker"}
	var err error
	a.Generation, err = s.ClaimAssignment(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "r", ActivationID: "act", State: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	events, err := s.GetEvents("r", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	after := events[len(events)-1].Seq
	for i := 0; i < 2; i++ {
		if err := s.RecordWorkerExecutionDuration(a, 75); err != nil {
			t.Fatal(err)
		}
	}
	events, err = s.GetEvents("r", after, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected one timing event, got %+v err=%v", events, err)
	}
	if events[0].Type != state.EventExecutionAttempt || events[0].Payload["executionDurationMs"] != json.Number("75") || events[0].Payload["state"] != "cancelled" {
		t.Fatalf("incorrect timing event: %+v", events[0])
	}
}
