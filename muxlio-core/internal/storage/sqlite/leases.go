package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

// Epoch checks happen inside the mutation transaction so an old coordinator
// cannot publish progress or claim work after its replacement has started.
func checkCoordinatorEpoch(tx *sql.Tx, epoch int64) error {
	if epoch == 0 {
		return nil
	} // legacy local assignment API
	var current int64
	if err := tx.QueryRow(`SELECT value FROM coordinator_meta WHERE key='epoch'`).Scan(&current); err != nil {
		return err
	}
	if epoch != current {
		return fmt.Errorf("coordinator epoch is stale")
	}
	return nil
}

// recoverLostDispatches releases execution capacity, but only explicitly pure
// operations may be replayed. Unknown effect policies fail closed.
func recoverLostDispatches(tx *sql.Tx) error {
	if _, err := tx.Exec(`UPDATE assignments SET state='uncertain' WHERE state='active' AND lease_until<=?`, rfc3339(time.Now())); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE dispatches SET state='uncertain' WHERE state='assigned' AND activation_id IN (SELECT activation_id FROM assignments WHERE state='uncertain')`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT d.activation_id,d.request_json FROM dispatches d JOIN assignments a ON a.activation_id=d.activation_id WHERE d.state='uncertain' AND a.state='uncertain'`)
	if err != nil {
		return err
	}
	var pure []string
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var request struct {
			Effect string `json:"effect"`
		}
		if json.Unmarshal([]byte(raw), &request) == nil && request.Effect == "none" {
			pure = append(pure, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range pure {
		if _, err := tx.Exec(`UPDATE assignments SET state='cancelled' WHERE activation_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE dispatches SET state='pending' WHERE activation_id=?`, id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`DELETE FROM admission_reservations WHERE activation_id IN (SELECT activation_id FROM assignments WHERE state<>'active')`)
	return err
}

// RecoverWorkerLeases also runs when no worker is polling.
func (s *Store) RecoverWorkerLeases() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recoverLostDispatches(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenewWorkerLeases(workerID string, leases []state.AssignmentLease, now, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recoverLostDispatches(tx); err != nil {
		return err
	}
	for _, lease := range leases {
		if err := checkCoordinatorEpoch(tx, lease.CoordinatorEpoch); err != nil {
			continue
		}
		if _, err := tx.Exec(`UPDATE assignments SET lease_until=? WHERE worker_id=? AND assignment_id=? AND generation=? AND coordinator_epoch=? AND state='active' AND lease_until>?`, rfc3339(until), workerID, lease.AssignmentID, lease.Generation, lease.CoordinatorEpoch, rfc3339(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AppendWorkerLogChunk makes validation and publication one fenced transition.
func (s *Store) AppendWorkerLogChunk(a state.Assignment, chunk state.LogChunk) (bool, error) {
	if a.RunID != chunk.RunID || a.ActivationID != chunk.ActivationID {
		return false, fmt.Errorf("log identity does not match assignment")
	}
	if len(chunk.Data) > 64<<10 {
		return false, fmt.Errorf("log chunk exceeds 64 KiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := checkCoordinatorEpoch(tx, a.CoordinatorEpoch); err != nil {
		return false, err
	}
	res, err := tx.Exec(`INSERT INTO log_chunks(run_id,activation_id,stream,seq,data,timestamp)
 SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM assignments WHERE run_id=? AND activation_id=? AND worker_id=? AND assignment_id=? AND attempt_id=? AND generation=? AND coordinator_epoch=? AND state='active' AND lease_until>?)`,
		chunk.RunID, chunk.ActivationID, chunk.Stream, chunk.Seq, chunk.Data, rfc3339(chunk.Timestamp),
		a.RunID, a.ActivationID, a.WorkerID, a.AssignmentID, a.AttemptID, a.Generation, a.CoordinatorEpoch, rfc3339(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
}
