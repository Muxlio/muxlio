package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func (s *Store) MarkExecutorUnavailable(binding state.ExecutorBinding, report contract.Availability, now time.Time) error {
	return markExecutorUnavailable(s.db, binding, report, now)
}

func markExecutorUnavailable(db interface {
	Exec(string, ...any) (sql.Result, error)
}, binding state.ExecutorBinding, report contract.Availability, now time.Time) error {
	if err := report.Validate(); err != nil {
		return err
	}
	if binding.WorkerID == "" || binding.Executor == "" {
		return fmt.Errorf("muxlio: unavailable binding requires worker and executor")
	}
	var retryAt any
	if report.RetryAt != nil {
		retryAt = report.RetryAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := db.Exec(`INSERT INTO executor_unavailability(worker_id,executor,reason,message,retry_at,updated_at,recheck_held_until)
		VALUES(?,?,?,?,?,?,NULL)
		ON CONFLICT(worker_id,executor) DO UPDATE SET reason=excluded.reason,message=excluded.message,retry_at=excluded.retry_at,updated_at=excluded.updated_at,recheck_held_until=NULL`,
		binding.WorkerID, binding.Executor, report.Reason, report.Message, retryAt, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetExecutorUnavailability(binding state.ExecutorBinding) (*state.ExecutorUnavailability, error) {
	row := s.db.QueryRow(`SELECT worker_id,executor,reason,message,retry_at,updated_at,recheck_held_until FROM executor_unavailability WHERE worker_id=? AND executor=?`,
		binding.WorkerID, binding.Executor)
	return scanUnavailability(row)
}

func executorUnavailableLocked(tx *sql.Tx, workerID, executor string, now time.Time) (bool, error) {
	var reason string
	var retryAt, held sql.NullString
	err := tx.QueryRow(`SELECT reason,retry_at,recheck_held_until FROM executor_unavailability WHERE worker_id=? AND executor=?`, workerID, executor).Scan(&reason, &retryAt, &held)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if reason == "" {
		return false, nil
	}
	// Once the cooldown expires, admit one normal assignment through the
	// existing lease mechanism. While it runs, concurrent claims stay blocked.
	if retryAt.Valid && retryAt.String != "" {
		resetAt, parseErr := time.Parse(time.RFC3339Nano, retryAt.String)
		if parseErr != nil {
			return true, parseErr
		}
		if !now.Before(resetAt) {
			if held.Valid && held.String != "" {
				heldUntil, heldErr := time.Parse(time.RFC3339Nano, held.String)
				if heldErr != nil {
					return true, heldErr
				}
				if now.Before(heldUntil) {
					return true, nil
				}
			}
			var active bool
			err := tx.QueryRow(`SELECT EXISTS(
				SELECT 1 FROM assignments a JOIN dispatches d ON d.attempt_id=a.attempt_id
				WHERE a.worker_id=? AND d.executor=? AND a.state='active' AND a.lease_until>?
			)`, workerID, executor, leaseTimestamp(now)).Scan(&active)
			return active, err
		}
	}
	return true, nil
}

func availabilityTrial(tx *sql.Tx, workerID, executor string) (*time.Time, error) {
	var updated string
	err := tx.QueryRow(`SELECT updated_at FROM executor_unavailability WHERE worker_id=? AND executor=?`,
		workerID, executor).Scan(&updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, updated)
	return &t, err
}

// Apply evidence in the same transaction as the accepted worker receipt. A
// successful trial can clear only the block it observed at claim time.
func completeExecutorAvailability(tx *sql.Tx, workerID string, req workers.ExecuteRequest, terminalState string, result []byte, now time.Time) error {
	var completion struct {
		Availability    *contract.Availability `json:"availability"`
		Error           string                 `json:"error"`
		WorkerStartedAt *time.Time             `json:"workerStartedAt"`
	}
	if err := json.Unmarshal(result, &completion); err != nil {
		return err
	}
	binding := state.ExecutorBinding{WorkerID: workerID, Executor: req.Executor}
	if report := completion.Availability; report != nil {
		normalized := workers.AvailabilityWithCooldown(*report, now)
		return markExecutorUnavailable(tx, binding, normalized, now)
	}
	if req.AvailabilityTrial == nil || req.Action != "" {
		return nil
	}
	version := rfc3339(*req.AvailabilityTrial)
	if terminalState == "succeeded" && completion.Error == "" && completion.WorkerStartedAt != nil && !completion.WorkerStartedAt.IsZero() {
		_, err := tx.Exec(`DELETE FROM executor_unavailability WHERE worker_id=? AND executor=? AND updated_at=?`,
			workerID, req.Executor, version)
		return err
	}
	_, err := tx.Exec(`UPDATE executor_unavailability SET retry_at=?,updated_at=?,recheck_held_until=NULL
		WHERE worker_id=? AND executor=? AND updated_at=?`,
		rfc3339(now.Add(workers.FailedRecheckBackoff)), rfc3339(now), workerID, req.Executor, version)
	return err
}

func (s *Store) ListExecutorUnavailability() ([]state.ExecutorUnavailability, error) {
	rows, err := s.db.Query(`SELECT worker_id,executor,reason,message,retry_at,updated_at,recheck_held_until FROM executor_unavailability ORDER BY worker_id,executor`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []state.ExecutorUnavailability
	for rows.Next() {
		u, err := scanUnavailability(rows)
		if err != nil {
			return nil, err
		}
		if u != nil {
			out = append(out, *u)
		}
	}
	return out, rows.Err()
}

func (s *Store) ClearExecutorUnavailable(binding state.ExecutorBinding) error {
	_, err := s.db.Exec(`DELETE FROM executor_unavailability WHERE worker_id=? AND executor=?`, binding.WorkerID, binding.Executor)
	return err
}

func (s *Store) TryBeginExecutorRecheck(binding state.ExecutorBinding, now time.Time, hold time.Duration) (bool, *state.ExecutorUnavailability, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRow(`SELECT worker_id,executor,reason,message,retry_at,updated_at,recheck_held_until FROM executor_unavailability WHERE worker_id=? AND executor=?`,
		binding.WorkerID, binding.Executor)
	u, err := scanUnavailability(row)
	if err != nil {
		return false, nil, err
	}
	if u == nil {
		return false, nil, tx.Commit()
	}
	if u.RecheckHeldUntil != nil && now.Before(*u.RecheckHeldUntil) {
		return false, u, tx.Commit()
	}
	until := now.Add(hold).UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE executor_unavailability SET recheck_held_until=? WHERE worker_id=? AND executor=?`, until, binding.WorkerID, binding.Executor); err != nil {
		return false, nil, err
	}
	held := now.Add(hold)
	u.RecheckHeldUntil = &held
	return true, u, tx.Commit()
}

func (s *Store) FinishExecutorRecheck(binding state.ExecutorBinding, clear bool, report *contract.Availability, now time.Time) error {
	if clear {
		result, err := s.db.Exec(`DELETE FROM executor_unavailability
			WHERE worker_id=? AND executor=? AND reason=? AND recheck_held_until IS NOT NULL`,
			binding.WorkerID, binding.Executor, contract.AvailabilityAuthenticationFailed)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err == nil && deleted == 0 {
			return state.ErrExecutorUnavailable
		}
		return err
	}
	if report != nil {
		if err := report.Validate(); err != nil {
			return err
		}
		return s.MarkExecutorUnavailable(binding, *report, now)
	}
	_, err := s.db.Exec(`UPDATE executor_unavailability SET recheck_held_until=NULL, updated_at=? WHERE worker_id=? AND executor=?`,
		now.UTC().Format(time.RFC3339Nano), binding.WorkerID, binding.Executor)
	return err
}

type availabilityScanner interface {
	Scan(dest ...any) error
}

func scanUnavailability(row availabilityScanner) (*state.ExecutorUnavailability, error) {
	var u state.ExecutorUnavailability
	var retryAt, held sql.NullString
	var updated string
	err := row.Scan(&u.WorkerID, &u.Executor, &u.Reason, &u.Message, &retryAt, &updated, &held)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return nil, err
	}
	if retryAt.Valid && retryAt.String != "" {
		t, err := time.Parse(time.RFC3339Nano, retryAt.String)
		if err != nil {
			return nil, err
		}
		u.RetryAt = &t
	}
	if held.Valid && held.String != "" {
		t, err := time.Parse(time.RFC3339Nano, held.String)
		if err != nil {
			return nil, err
		}
		u.RecheckHeldUntil = &t
	}
	return &u, nil
}
