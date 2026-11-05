package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/muxlio/muxlio-core/internal/workers"
)

// CancelAttemptWork fences one attempt without revoking siblings or claiming
// that the worker process or its external effects have stopped.
func (s *Store) CancelAttemptWork(attemptID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("store closed")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE assignments SET state='cancelled',generation=generation+1 WHERE attempt_id=? AND state IN ('active','uncertain')`, attemptID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE dispatches SET state='cancelled' WHERE attempt_id=? AND state IN ('pending','assigned','uncertain')`, attemptID); err != nil {
		return err
	}
	return tx.Commit()
}

// ExpireQueuedAttempt serializes the queue timeout with admission. Once claimed,
// only execution timeout or the run deadline applies.
func (s *Store) ExpireQueuedAttempt(attemptID string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, errors.New("store closed")
	}
	var raw string
	err := s.db.QueryRow(`SELECT request_json FROM dispatches WHERE attempt_id=? AND state IN ('pending','cancelled')`, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var req workers.ExecuteRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return false, err
	}
	if req.ExecutionDeadline != nil || req.QueueDeadline == nil || now.Before(*req.QueueDeadline) {
		return false, nil
	}
	_, err = s.db.Exec(`UPDATE dispatches SET state='cancelled' WHERE attempt_id=? AND state='pending'`, attemptID)
	return err == nil, err
}

// ExpireExecutingAttempt fences a timed-out remote attempt even if the worker
// cannot interrupt its executor. Later completion remains diagnostic only.
func (s *Store) ExpireExecutingAttempt(attemptID string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, errors.New("store closed")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRow(`SELECT request_json FROM dispatches WHERE attempt_id=? AND state IN ('assigned','uncertain','cancelled')`, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var req workers.ExecuteRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return false, err
	}
	if req.ExecutionDeadline == nil || now.Before(*req.ExecutionDeadline) {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE assignments SET state='cancelled',generation=generation+1 WHERE attempt_id=? AND state IN ('active','uncertain')`, attemptID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE dispatches SET state='cancelled' WHERE attempt_id=? AND state IN ('assigned','uncertain')`, attemptID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
