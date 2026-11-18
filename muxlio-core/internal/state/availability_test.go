package state_test

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestMemoryAvailabilityPersistsAcrossLookup(t *testing.T) {
	s := state.New()
	binding := state.ExecutorBinding{WorkerID: "worker", Executor: "agent/test"}
	retry := time.Now().Add(time.Hour)
	if err := s.MarkExecutorUnavailable(binding, contract.Availability{
		Reason:  contract.AvailabilityQuotaExhausted,
		Message: "provider quota exhausted",
		RetryAt: &retry,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetExecutorUnavailability(binding)
	if err != nil || got == nil || got.Reason != contract.AvailabilityQuotaExhausted {
		t.Fatalf("got %+v err %v", got, err)
	}
	other, err := s.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: "worker", Executor: "agent/other"})
	if err != nil || other != nil {
		t.Fatalf("unrelated binding should remain eligible: %+v %v", other, err)
	}
}

func TestMemoryAvailabilityRecheckFence(t *testing.T) {
	s := state.New()
	binding := state.ExecutorBinding{WorkerID: "worker", Executor: "agent/test"}
	now := time.Now()
	if err := s.MarkExecutorUnavailable(binding, contract.Availability{Reason: contract.AvailabilityAuthenticationFailed}, now); err != nil {
		t.Fatal(err)
	}
	ok, _, err := s.TryBeginExecutorRecheck(binding, now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first recheck: %v %v", ok, err)
	}
	ok, _, err = s.TryBeginExecutorRecheck(binding, now.Add(time.Second), time.Minute)
	if err != nil || ok {
		t.Fatalf("concurrent recheck must be fenced: %v %v", ok, err)
	}
	if err := s.FinishExecutorRecheck(binding, true, nil, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetExecutorUnavailability(binding)
	if err != nil || got != nil {
		t.Fatalf("cleared: %+v %v", got, err)
	}
}
