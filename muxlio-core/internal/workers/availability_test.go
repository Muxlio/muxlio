package workers

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestRecordAndBlockAvailability(t *testing.T) {
	store := state.New()
	report := &contract.Availability{Reason: contract.AvailabilityQuotaExhausted, Message: "provider quota exhausted"}
	if err := RecordAvailability(store, "worker", "agent/test", report, time.Now()); err != nil {
		t.Fatal(err)
	}
	blocked, err := ExecutorBlocked(store, "worker", "agent/test", time.Now())
	if err != nil || blocked == nil {
		t.Fatalf("expected block: %+v %v", blocked, err)
	}
	if mismatch := AvailabilityMismatch(blocked); mismatch == nil || mismatch.Code != "executor_unavailable" {
		t.Fatalf("mismatch: %+v", mismatch)
	}
	other, err := ExecutorBlocked(store, "worker", "agent/other", time.Now())
	if err != nil || other != nil {
		t.Fatalf("unrelated remains eligible: %+v %v", other, err)
	}
}

func TestRecheckExecutorClearsAndRefreshes(t *testing.T) {
	store := state.New()
	bindingWorker, executor := "worker", "agent/test"
	now := time.Now()
	if err := RecordAvailability(store, bindingWorker, executor, &contract.Availability{Reason: contract.AvailabilityAuthenticationFailed}, now); err != nil {
		t.Fatal(err)
	}
	if err := RecheckExecutor(store, bindingWorker, executor, now, true, func() (*contract.Availability, error) {
		return &contract.Availability{Reason: contract.AvailabilityAuthenticationFailed, Message: "still broken"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	blocked, _ := ExecutorBlocked(store, bindingWorker, executor, now.Add(time.Minute))
	if blocked == nil {
		t.Fatal("repeated rejection must keep unavailable")
	}
	if err := RecheckExecutor(store, bindingWorker, executor, now.Add(2*time.Minute), true, func() (*contract.Availability, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	blocked, _ = ExecutorBlocked(store, bindingWorker, executor, now.Add(3*time.Minute))
	if blocked != nil {
		t.Fatalf("cleared: %+v", blocked)
	}
}

func TestUnknownAvailabilityIsIgnored(t *testing.T) {
	store := state.New()
	if err := RecordAvailability(store, "worker", "agent/test", &contract.Availability{Reason: "nope"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	blocked, err := ExecutorBlocked(store, "worker", "agent/test", time.Now())
	if err != nil || blocked != nil {
		t.Fatalf("invalid report must not block: %+v %v", blocked, err)
	}
}

func TestRecordAvailabilityDefaultsQuotaRetryAt(t *testing.T) {
	store := state.New()
	now := time.Now()
	if err := RecordAvailability(store, "worker", "agent/test", &contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "quota",
	}, now); err != nil {
		t.Fatal(err)
	}
	blocked, err := ExecutorBlocked(store, "worker", "agent/test", now)
	if err != nil || blocked == nil || blocked.RetryAt == nil {
		t.Fatalf("quota without RetryAt must default one: %+v %v", blocked, err)
	}
	if delta := blocked.RetryAt.Sub(now); delta < DefaultQuotaRetryAfter-time.Second || delta > DefaultQuotaRetryAfter+time.Second {
		t.Fatalf("RetryAt delta=%v want %v", delta, DefaultQuotaRetryAfter)
	}
}
