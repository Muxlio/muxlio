package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestExecutorUnavailabilitySurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "muxlio.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	binding := state.ExecutorBinding{WorkerID: "worker", Executor: "agent/test"}
	retry := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	if err := s.MarkExecutorUnavailable(binding, contract.Availability{
		Reason:  contract.AvailabilityQuotaExhausted,
		Message: "provider quota exhausted",
		RetryAt: &retry,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetExecutorUnavailability(binding)
	if err != nil || got == nil || got.Reason != contract.AvailabilityQuotaExhausted {
		t.Fatalf("persisted: %+v %v", got, err)
	}
	other, err := s.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: "other", Executor: "agent/test"})
	if err != nil || other != nil {
		t.Fatalf("unrelated binding: %+v %v", other, err)
	}
}
