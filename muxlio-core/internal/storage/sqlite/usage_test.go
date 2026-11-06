package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestMeasurementsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	tokens := int64(9)
	cost := 0.25
	estimate := 0.5
	a := state.ExecutionAttempt{ID: "a", RunID: "r", ActivationID: "act", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Measurements: &contract.Measurements{EstimatedTokens: &tokens, CostUSD: &cost, EstimatedCostUSD: &estimate}}
	if err = s.RecordExecutionAttempt(a); err != nil {
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
	attempts, err := s.ListExecutionAttempts("r", "")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	got := attempts[0]
	if got.Usage != nil || got.Measurements == nil || *got.Measurements.EstimatedTokens != 9 || *got.Measurements.CostUSD != 0.25 || *got.Measurements.EstimatedCostUSD != 0.5 {
		t.Fatalf("attempt=%+v", got)
	}
}
