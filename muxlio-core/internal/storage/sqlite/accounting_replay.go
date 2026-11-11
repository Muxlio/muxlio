package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

type replayBudgetError struct{ error }

// prepareWorkerReplay keeps physical executions disjoint even when the durable
// dispatch retains its logical attempt ID for the waiting coordinator. It runs
// in the claim transaction, before assigning the replacement worker.
func prepareWorkerReplay(tx *sql.Tx, d state.Dispatch, generation int64) (bool, error) {
	var previousID string
	err := tx.QueryRow(`SELECT attempt_id FROM assignments WHERE activation_id=?`, d.ActivationID).Scan(&previousID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if previousID != d.AttemptID {
		return false, nil
	}
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM execution_attempts WHERE id=?)`, d.AttemptID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}

	if err := checkWorkerReplayLimits(tx, d); err != nil {
		var blocked replayBudgetError
		if !errors.As(err, &blocked) {
			return false, err
		}
		// Return a durable failure to the original waiter instead of silently
		// leaving the dispatch queued or executing beyond a pinned budget.
		raw, marshalErr := json.Marshal(map[string]any{"error": err.Error()})
		if marshalErr != nil {
			return false, marshalErr
		}
		if _, e := tx.Exec(`INSERT INTO worker_results(attempt_id,result_json) VALUES(?,?) ON CONFLICT DO NOTHING`, d.AttemptID, raw); e != nil {
			return false, e
		}
		if _, e := tx.Exec(`UPDATE dispatches SET state='completed' WHERE attempt_id=?`, d.AttemptID); e != nil {
			return false, e
		}
		if _, e := tx.Exec(`UPDATE assignments SET state='failed',generation=generation+1 WHERE attempt_id=?`, d.AttemptID); e != nil {
			return false, e
		}
		return true, appendEvent(tx, d.RunID, "execution.replay_blocked", d.ActivationID, map[string]any{"attemptId": d.AttemptID, "reason": err.Error()})
	}
	archiveID := fmt.Sprintf("worker-execution-%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d", d.AttemptID, generation))))
	// Retain non-final accounting as non-final: losing a worker does not prove
	// that its usage report covered all consumption or that it stopped.
	_, err = tx.Exec(`INSERT INTO execution_attempts(id,run_id,activation_id,retry_number,executor,state,started_at,finished_at,failure,effect,idempotency_key,accounting_json,accounting_revision,accounting_final,accounting_hash,accounting_assignment,accounting_generation)
	 SELECT ?,run_id,activation_id,retry_number,executor,'failed',started_at,?,'Worker lost; termination and final consumption unconfirmed',effect,idempotency_key,accounting_json,accounting_revision,accounting_final,accounting_hash,accounting_assignment,accounting_generation FROM execution_attempts WHERE id=?`, archiveID, rfc3339(time.Now()), d.AttemptID)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(`UPDATE execution_attempts SET accounting_json=NULL,accounting_revision=0,accounting_final=0,accounting_hash=NULL,accounting_assignment=NULL,accounting_generation=NULL WHERE id=?`, d.AttemptID)
	if err != nil {
		return false, err
	}
	return false, appendEvent(tx, d.RunID, "execution.worker_replay", d.ActivationID, map[string]any{"attemptId": d.AttemptID, "previousExecutionId": archiveID, "generation": generation + 1})
}

func checkWorkerReplayLimits(tx *sql.Tx, d state.Dispatch) error {
	var raw sql.NullString
	if err := tx.QueryRow(`SELECT plan_json FROM runs WHERE id=?`, d.RunID).Scan(&raw); err != nil {
		return err
	}
	var plan struct {
		Limits *struct {
			Accounting        []accounting.Limit `json:"accounting"`
			MaxAttempts       *int               `json:"maxAttempts"`
			MaxActiveAttempts *int               `json:"maxActiveAttempts"`
		} `json:"limits"`
	}
	if !raw.Valid || raw.String == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw.String), &plan); err != nil {
		return err
	}
	if plan.Limits == nil {
		return nil
	}
	rows, err := tx.Query(`SELECT id,state,accounting_json FROM execution_attempts WHERE run_id=?`, d.RunID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var views []accounting.AttemptView
	active := 0
	for rows.Next() {
		var id, status string
		var snapshot sql.NullString
		if err := rows.Scan(&id, &status, &snapshot); err != nil {
			return err
		}
		if id == d.AttemptID {
			status = "failed"
		}
		if status == "queued" || status == "running" || status == "awaiting_reconciliation" || status == "cancelled" {
			active++
		}
		v := accounting.AttemptView{State: status}
		if snapshot.Valid {
			v.Accounting = &contract.Accounting{}
			if err := json.Unmarshal([]byte(snapshot.String), v.Accounting); err != nil {
				return err
			}
		}
		views = append(views, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n := plan.Limits.MaxAttempts; n != nil && len(views) >= *n {
		return replayBudgetError{fmt.Errorf("budget-exhausted: maxAttempts")}
	}
	if n := plan.Limits.MaxActiveAttempts; n != nil && active >= *n {
		return replayBudgetError{fmt.Errorf("budget-exhausted: maxActiveAttempts")}
	}
	for _, limit := range plan.Limits.Accounting {
		if err := accounting.CheckLimit(views, limit, true); err != nil {
			return replayBudgetError{err}
		}
	}
	return nil
}
