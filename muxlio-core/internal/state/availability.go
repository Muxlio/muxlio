package state

import (
	"errors"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

// ErrExecutorUnavailable is returned when admission rejects a blocked
// worker/executor binding before a new attempt is recorded.
var ErrExecutorUnavailable = errors.New("muxlio: executor unavailable")

// ExecutorBinding is the only trustworthy unavailability identity a worker can
// establish: the local worker id plus the executor name. Narrower account or
// model scopes are not claimed without matching request and failure evidence.
type ExecutorBinding struct {
	WorkerID string `json:"workerId"`
	Executor string `json:"executor"`
}

// ExecutorUnavailability persists a confirmed adapter availability signal
// outside a single run so future admission can skip the binding.
type ExecutorUnavailability struct {
	WorkerID         string     `json:"workerId"`
	Executor         string     `json:"executor"`
	Reason           string     `json:"reason"`
	Message          string     `json:"message,omitempty"`
	RetryAt          *time.Time `json:"retryAt,omitempty"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	RecheckHeldUntil *time.Time `json:"recheckHeldUntil,omitempty"`
}

// AvailabilityFromReport builds a durable unavailability row from an adapter
// report. Message must never contain credentials.
type AvailabilityStore interface {
	MarkExecutorUnavailable(binding ExecutorBinding, report contract.Availability, now time.Time) error
	GetExecutorUnavailability(binding ExecutorBinding) (*ExecutorUnavailability, error)
	ListExecutorUnavailability() ([]ExecutorUnavailability, error)
	ClearExecutorUnavailable(binding ExecutorBinding) error
	// TryBeginExecutorRecheck fences concurrent rechecks. It returns true when
	// this caller holds the recheck window and should probe.
	TryBeginExecutorRecheck(binding ExecutorBinding, now time.Time, hold time.Duration) (bool, *ExecutorUnavailability, error)
	FinishExecutorRecheck(binding ExecutorBinding, clear bool, report *contract.Availability, now time.Time) error
}

// AvailabilityBlocks reports whether a stored unavailability still blocks
// execution until recovery is recorded.
func AvailabilityBlocks(u *ExecutorUnavailability, now time.Time) bool {
	return u != nil && u.Reason != ""
}

// AvailabilityBlocksAdmission reports whether admission should skip the binding
// entirely. Past RetryAt (with no active recheck hold) is allowed through so a
// coordinator can admit one normal assignment to verify recovery.
func AvailabilityBlocksAdmission(u *ExecutorUnavailability, now time.Time) bool {
	if !AvailabilityBlocks(u, now) {
		return false
	}
	if AvailabilityReadyForRecheck(u, now, false) {
		return false
	}
	return true
}

// AvailabilityReadyForRecheck is true when a known reset has elapsed or the
// caller requested an explicit recheck of an auth failure.
func AvailabilityReadyForRecheck(u *ExecutorUnavailability, now time.Time, explicit bool) bool {
	if u == nil {
		return false
	}
	if u.RecheckHeldUntil != nil && now.Before(*u.RecheckHeldUntil) {
		return false
	}
	if explicit {
		return true
	}
	return u.RetryAt != nil && !now.Before(*u.RetryAt)
}
