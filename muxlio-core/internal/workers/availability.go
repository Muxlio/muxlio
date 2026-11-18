package workers

import (
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

const (
	availabilityRecheckHold = 30 * time.Second
	// DefaultQuotaRetryAfter applies when a quota report omits RetryAt, so
	// scheduled recovery probes can run instead of permanently locking the binding.
	DefaultQuotaRetryAfter = time.Hour
	// FailedRecheckBackoff pushes RetryAt forward after an inconclusive recovery attempt
	// so claim does not immediately re-admit the same exhausted binding.
	FailedRecheckBackoff = 15 * time.Minute
)

// AvailabilityStore is the durable worker/executor unavailability surface.
type AvailabilityStore = state.AvailabilityStore

// RecordAvailability persists a confirmed adapter availability report for the
// worker/executor binding. Unknown or invalid reports are ignored. Quota reports
// without RetryAt receive DefaultQuotaRetryAfter so recovery remains possible.
func RecordAvailability(store AvailabilityStore, workerID, executor string, report *contract.Availability, now time.Time) error {
	if store == nil || report == nil {
		return nil
	}
	if err := report.Validate(); err != nil {
		return nil
	}
	if workerID == "" {
		workerID = "local"
	}
	return store.MarkExecutorUnavailable(state.ExecutorBinding{WorkerID: workerID, Executor: executor}, AvailabilityWithCooldown(*report, now), now)
}

// AvailabilityWithCooldown supplies a retry time for quota reports without a provider reset.
func AvailabilityWithCooldown(report contract.Availability, now time.Time) contract.Availability {
	if report.Reason == contract.AvailabilityQuotaExhausted && report.RetryAt == nil {
		retry := now.Add(DefaultQuotaRetryAfter)
		report.RetryAt = &retry
	}
	return report
}

// ExecutorBlocked reports whether the binding is currently excluded from
// admission pending recovery.
func ExecutorBlocked(store AvailabilityStore, workerID, executor string, now time.Time) (*state.ExecutorUnavailability, error) {
	if store == nil {
		return nil, nil
	}
	if workerID == "" {
		workerID = "local"
	}
	u, err := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: workerID, Executor: executor})
	if err != nil || u == nil {
		return u, err
	}
	if state.AvailabilityBlocks(u, now) {
		return u, nil
	}
	return nil, nil
}

// AvailabilityMismatch builds a dispatch mismatch for a blocked binding.
func AvailabilityMismatch(u *state.ExecutorUnavailability) *DispatchMismatch {
	if u == nil {
		return nil
	}
	msg := u.Message
	if msg == "" {
		msg = "Executor is unavailable: " + u.Reason
	}
	if u.RetryAt != nil {
		msg = fmt.Sprintf("%s (retry after %s)", msg, u.RetryAt.UTC().Format(time.RFC3339))
	}
	return &DispatchMismatch{Code: "executor_unavailable", Message: msg, Capability: u.Executor}
}

// RecheckExecutor applies an operator recheck for authentication blocks.
// Quota recovery requires a successful assignment after the cooldown.
func RecheckExecutor(store AvailabilityStore, workerID, executor string, now time.Time, explicit bool, probe func() (*contract.Availability, error)) error {
	if store == nil {
		return fmt.Errorf("muxlio: availability store unavailable")
	}
	if workerID == "" {
		workerID = "local"
	}
	binding := state.ExecutorBinding{WorkerID: workerID, Executor: executor}
	u, err := store.GetExecutorUnavailability(binding)
	if err != nil {
		return err
	}
	if u == nil {
		return nil
	}
	if u.Reason == contract.AvailabilityQuotaExhausted {
		return fmt.Errorf("%w: quota recovery requires an assignment", state.ErrExecutorUnavailable)
	}
	if !state.AvailabilityReadyForRecheck(u, now, explicit) {
		return fmt.Errorf("%w: %s", state.ErrExecutorUnavailable, AvailabilityMismatch(u).Message)
	}
	begun, _, err := store.TryBeginExecutorRecheck(binding, now, availabilityRecheckHold)
	if err != nil {
		return err
	}
	if !begun {
		return fmt.Errorf("muxlio: executor recheck already in progress")
	}
	report, probeErr := probe()
	if probeErr != nil {
		// Keep admission blocked: clear the fence and push RetryAt forward so
		// claim does not thrash on the same past reset window.
		retry := now.Add(FailedRecheckBackoff)
		refreshed := contract.Availability{Reason: u.Reason, Message: u.Message, RetryAt: &retry}
		if refreshed.Reason == "" {
			refreshed.Reason = contract.AvailabilityQuotaExhausted
		}
		_ = store.FinishExecutorRecheck(binding, false, &refreshed, now)
		return probeErr
	}
	if report != nil {
		return store.FinishExecutorRecheck(binding, false, report, now)
	}
	return store.FinishExecutorRecheck(binding, true, nil, now)
}
