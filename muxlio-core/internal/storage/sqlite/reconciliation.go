package sqlite

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/state"
)

func (s *Store) ResolveExecutionAttempt(runID, attemptID string, r state.AttemptResolution) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var activation, reason string
	if err := tx.QueryRow(`SELECT activation_id,await_reconciliation_reason FROM execution_attempts WHERE run_id=? AND id=?`, runID, attemptID).Scan(&activation, &reason); err != nil {
		return err
	}
	if reason == "" {
		return fmt.Errorf("attempt is not awaiting reconciliation")
	}
	next := "resolved"
	if r.Outcome == "completed" {
		next = "succeeded"
	}
	var result any
	if r.Result != nil {
		raw, err := json.Marshal(r.Result)
		if err != nil {
			return err
		}
		result = string(raw)
	}
	if _, err = tx.Exec(`UPDATE execution_attempts SET state=?,failure='',await_reconciliation_reason='',result_json=? WHERE id=?`, next, result, attemptID); err != nil {
		return err
	}
	// Fence the lost worker and release only this operation's resources atomically.
	if _, err = tx.Exec(`UPDATE assignments SET state='cancelled',generation=generation+1 WHERE attempt_id=?`, attemptID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE dispatches SET state='completed' WHERE attempt_id=?`, attemptID); err != nil {
		return err
	}
	for _, table := range []string{"exclusive_resources", "admission_reservations"} {
		if _, err = tx.Exec(`DELETE FROM `+table+` WHERE activation_id=?`, activation); err != nil {
			return err
		}
	}
	if err = appendEvent(tx, runID, "execution.resolved", activation, map[string]any{"attemptId": attemptID, "outcome": r.Outcome, "reason": r.Reason}); err != nil {
		return err
	}
	return tx.Commit()
}
