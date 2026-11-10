package state_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestRecordExecutionAttemptPreservesObservationsOnReplaySQLite(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "attempts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	cost := 0.5
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "act", State: "running",
		StartedAt: time.Now(), Usage: &state.TokenUsage{Input: 10, Output: 5},
		Measurements: &contract.Measurements{CostUSD: &cost},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "act", State: "succeeded",
		StartedAt: time.Now(), FinishedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("r", "act")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	if attempts[0].Usage == nil || attempts[0].Usage.Input != 10 {
		t.Fatalf("usage lost on replay: %+v", attempts[0].Usage)
	}
	if attempts[0].Measurements == nil || attempts[0].Measurements.CostUSD == nil {
		t.Fatalf("measurements lost on replay: %+v", attempts[0].Measurements)
	}
}

func TestRecordExecutionAttemptPreservesCreditsOnReplaySQLite(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "attempts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	credit := 0.18065595854063016
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "act", State: "running",
		StartedAt: time.Now(),
		Measurements: &contract.Measurements{Credit: &contract.ProviderCredit{Unit: "credit", Amount: &credit, Provenance: contract.CreditProvenancePerTurn}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "act", State: "failed",
		StartedAt: time.Now(), FinishedAt: time.Now(), Failure: "interrupted",
	}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("r", "act")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	if attempts[0].Measurements == nil || attempts[0].Measurements.Credit == nil || attempts[0].Measurements.Credit.Amount == nil {
		t.Fatalf("credit measurements lost on replay: %+v", attempts[0].Measurements)
	}
}
