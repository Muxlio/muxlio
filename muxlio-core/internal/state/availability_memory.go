package state

import (
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

func availabilityKey(b ExecutorBinding) string {
	return b.WorkerID + "\x00" + b.Executor
}

func (s *Memory) ensureAvailability() {
	if s.unavailable == nil {
		s.unavailable = make(map[string]*ExecutorUnavailability)
	}
}

// MarkExecutorUnavailable records or refreshes a confirmed unavailability.
func (s *Memory) MarkExecutorUnavailable(binding ExecutorBinding, report contract.Availability, now time.Time) error {
	if err := report.Validate(); err != nil {
		return err
	}
	if binding.WorkerID == "" || binding.Executor == "" {
		return fmt.Errorf("muxlio: unavailable binding requires worker and executor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAvailability()
	row := &ExecutorUnavailability{
		WorkerID:  binding.WorkerID,
		Executor:  binding.Executor,
		Reason:    report.Reason,
		Message:   report.Message,
		RetryAt:   report.RetryAt,
		UpdatedAt: now,
	}
	s.unavailable[availabilityKey(binding)] = row
	return nil
}

func (s *Memory) GetExecutorUnavailability(binding ExecutorBinding) (*ExecutorUnavailability, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.unavailable == nil {
		return nil, nil
	}
	row := s.unavailable[availabilityKey(binding)]
	if row == nil {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

func (s *Memory) ListExecutorUnavailability() ([]ExecutorUnavailability, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExecutorUnavailability, 0, len(s.unavailable))
	for _, row := range s.unavailable {
		out = append(out, *row)
	}
	return out, nil
}

func (s *Memory) ClearExecutorUnavailable(binding ExecutorBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAvailability()
	delete(s.unavailable, availabilityKey(binding))
	return nil
}

func (s *Memory) TryBeginExecutorRecheck(binding ExecutorBinding, now time.Time, hold time.Duration) (bool, *ExecutorUnavailability, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAvailability()
	row := s.unavailable[availabilityKey(binding)]
	if row == nil {
		return false, nil, nil
	}
	if row.RecheckHeldUntil != nil && now.Before(*row.RecheckHeldUntil) {
		copy := *row
		return false, &copy, nil
	}
	until := now.Add(hold)
	row.RecheckHeldUntil = &until
	copy := *row
	return true, &copy, nil
}

func (s *Memory) FinishExecutorRecheck(binding ExecutorBinding, clear bool, report *contract.Availability, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureAvailability()
	key := availabilityKey(binding)
	row := s.unavailable[key]
	if clear {
		if row == nil || row.Reason != contract.AvailabilityAuthenticationFailed || row.RecheckHeldUntil == nil {
			return ErrExecutorUnavailable
		}
		delete(s.unavailable, key)
		return nil
	}
	if row == nil {
		return nil
	}
	row.RecheckHeldUntil = nil
	row.UpdatedAt = now
	if report != nil {
		if err := report.Validate(); err != nil {
			return err
		}
		row.Reason = report.Reason
		row.Message = report.Message
		row.RetryAt = report.RetryAt
	}
	return nil
}
