package state

import "fmt"

// AttemptResolution records explicit evidence, from an adapter or operator.
// not-applied asserts that the previous operation cannot subsequently commit.
type AttemptResolution struct {
	Outcome string         `json:"outcome"`
	Reason  string         `json:"reason"`
	Result  *AttemptResult `json:"result,omitempty"`
}

// UncertainRetryReason holds an attempt found by explicit retry whose worker
// outcome is unknown and whose effect cannot be replayed safely.
const UncertainRetryReason = "execution outcome unknown after worker or coordinator loss; explicit resolution required before retry"

// ReplaySafeEffect reports whether repeating an attempt cannot duplicate an
// external effect. Unclassified effects are unsafe.
func ReplaySafeEffect(effect, idempotencyKey string) bool {
	return effect == "none" || effect == "pure" || ((effect == "idempotency-key" || effect == "idempotent-with-key") && idempotencyKey != "")
}

func (r AttemptResolution) Validate() error {
	if r.Reason == "" {
		return fmt.Errorf("resolution requires a reason")
	}
	switch r.Outcome {
	case "completed":
		if r.Result == nil || !r.Result.ExecutionSucceeded {
			return fmt.Errorf("completed resolution requires a successful result")
		}
	case "not-applied":
	default:
		return fmt.Errorf("resolution outcome must be completed or not-applied")
	}
	return nil
}

func (s *Memory) ResolveExecutionAttempt(runID, attemptID string, r AttemptResolution) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.attempts {
		a := &s.attempts[i]
		if a.RunID != runID || a.ID != attemptID {
			continue
		}
		if a.AwaitReconciliationReason == "" {
			return fmt.Errorf("attempt is not awaiting reconciliation")
		}
		a.AwaitReconciliationReason = ""
		a.State = "resolved"
		a.Failure = ""
		a.Result = r.Result
		if r.Outcome == "completed" {
			a.State = "succeeded"
		}
		if assignment := s.assignments[a.ActivationID]; assignment != nil && assignment.AttemptID == a.ID {
			assignment.State = "cancelled"
		}
		s.appendEventLocked(runID, "execution.resolved", a.ActivationID, map[string]any{"attemptId": attemptID, "outcome": r.Outcome, "reason": r.Reason})
		return nil
	}
	return fmt.Errorf("attempt not found")
}
