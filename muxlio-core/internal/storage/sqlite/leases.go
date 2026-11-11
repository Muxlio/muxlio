package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

// RecordWorkerAccountingSnapshot replaces one attempt snapshot under the same
// active lease fence used for worker log progress.
func (s *Store) RecordWorkerAccountingSnapshot(a state.Assignment, snapshot contract.Accounting) (bool, error) {
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
	var valid bool
	err = tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM assignments WHERE run_id=? AND activation_id=? AND worker_id=? AND assignment_id=? AND attempt_id=? AND generation=? AND coordinator_epoch=? AND state='active' AND lease_until>?)`, a.RunID, a.ActivationID, a.WorkerID, a.AssignmentID, a.AttemptID, a.Generation, a.CoordinatorEpoch, leaseTimestamp(time.Now())).Scan(&valid)
	if err != nil || !valid {
		return false, err
	}
	if err := recordWorkerAccountingTx(tx, a, snapshot); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Both progress and terminal completion call this after fencing the assignment
// in the same transaction. A completion receipt cannot commit ahead of its
// canonical accounting snapshot, nor seal a conflicting accounting revision.
func recordWorkerAccountingTx(tx *sql.Tx, a state.Assignment, snapshot contract.Accounting) error {
	raw, err := contract.CanonicalAccounting(snapshot)
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	var rev int64
	var final int
	var priorHash sql.NullString
	err = tx.QueryRow(`SELECT accounting_revision,accounting_final,accounting_hash FROM execution_attempts WHERE id=? AND run_id=? AND activation_id=?`, a.AttemptID, a.RunID, a.ActivationID).Scan(&rev, &final, &priorHash)
	if err == sql.ErrNoRows {
		return fmt.Errorf("accounting attempt does not exist")
	}
	if err != nil {
		return err
	}
	if int64(snapshot.Revision) < rev {
		return fmt.Errorf("stale accounting revision")
	}
	if int64(snapshot.Revision) == rev {
		if priorHash.Valid && priorHash.String == hash {
			return nil
		}
		return fmt.Errorf("accounting revision conflict")
	}
	if final != 0 {
		return fmt.Errorf("accounting snapshot is already final")
	}
	res, err := tx.Exec(`UPDATE execution_attempts SET accounting_json=?,accounting_revision=?,accounting_final=?,accounting_hash=?,accounting_assignment=?,accounting_generation=? WHERE id=? AND accounting_revision=? AND accounting_final=0`, string(raw), snapshot.Revision, boolInt(snapshot.Final), hash, a.AssignmentID, a.Generation, a.AttemptID, rev)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("accounting snapshot compare-and-swap failed")
	}
	if err := appendEvent(tx, a.RunID, state.EventAccountingUpdated, a.ActivationID, map[string]any{"attemptId": a.AttemptID, "revision": snapshot.Revision, "hash": hash}); err != nil {
		return err
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

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
	if _, err := tx.Exec(`UPDATE assignments SET state='uncertain' WHERE state='active' AND lease_until<=?`, leaseTimestamp(time.Now())); err != nil {
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
			Effect            string                   `json:"effect"`
			IdempotencyKey    string                   `json:"idempotencyKey"`
			Session           *contract.SessionRequest `json:"session"`
			ConcurrencyKeys   []string                 `json:"concurrencyKeys"`
			ExecutionDeadline *time.Time               `json:"executionDeadline"`
			Deadline          *time.Time               `json:"deadline"`
		}
		if json.Unmarshal([]byte(raw), &request) == nil && (request.Effect == "none" || request.Effect == "pure" || ((request.Effect == "idempotency-key" || request.Effect == "idempotent-with-key") && request.IdempotencyKey != "")) && len(request.ConcurrencyKeys) == 0 && request.Session == nil {
			// A lost worker must not restart an already expired attempt.
			now := time.Now()
			if (request.ExecutionDeadline != nil && !now.Before(*request.ExecutionDeadline)) || (request.Deadline != nil && !now.Before(*request.Deadline)) {
				continue
			}
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
		if _, err := tx.Exec(`UPDATE assignments SET lease_until=? WHERE worker_id=? AND assignment_id=? AND generation=? AND coordinator_epoch=? AND state='active' AND lease_until>?`, leaseTimestamp(until), workerID, lease.AssignmentID, lease.Generation, lease.CoordinatorEpoch, leaseTimestamp(now)); err != nil {
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
	var valid bool
	err = tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM assignments WHERE run_id=? AND activation_id=? AND worker_id=? AND assignment_id=? AND attempt_id=? AND generation=? AND coordinator_epoch=? AND state='active' AND lease_until>?)`, a.RunID, a.ActivationID, a.WorkerID, a.AssignmentID, a.AttemptID, a.Generation, a.CoordinatorEpoch, leaseTimestamp(time.Now())).Scan(&valid)
	if err != nil || !valid {
		return false, err
	}
	chunk.AttemptID = a.AttemptID
	chunk.AssignmentID, chunk.WorkerID = a.AssignmentID, a.WorkerID
	chunk.Generation, chunk.CoordinatorEpoch = a.Generation, a.CoordinatorEpoch
	if err := persistProgress(tx, chunk); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// persistProgress atomically publishes the replay event and compatibility log.
// Limits apply at ingestion as well as in workers, including across reconnects.
func persistProgress(tx *sql.Tx, c state.LogChunk) error {
	kind, subject, payload := state.ProgressEvent(c)
	var count, duplicate, size int64
	if err := tx.QueryRow(`SELECT count(*), coalesce(sum(CASE WHEN json_extract(payload_json,'$.stream')=? AND json_extract(payload_json,'$.sourceSeq')=? AND coalesce(json_extract(payload_json,'$.generation'),0)=? AND coalesce(json_extract(payload_json,'$.coordinatorEpoch'),0)=? THEN 1 ELSE 0 END),0), coalesce(sum(CASE WHEN type='artifact.snapshot' THEN json_extract(payload_json,'$.snapshot.size') ELSE length(cast(json_extract(payload_json,'$.text') AS BLOB)) END),0) FROM events WHERE run_id=? AND subject_id=? AND type=?`, c.Stream, c.Seq, c.Generation, c.CoordinatorEpoch, c.RunID, subject, kind).Scan(&count, &duplicate, &size); err != nil {
		return err
	}
	if duplicate > 0 {
		return nil
	}
	if c.Snapshot != nil {
		if count >= state.MaxAttemptSnapshots || c.Snapshot.Size < 0 || c.Snapshot.Size > 4<<20 || size+c.Snapshot.Size > state.MaxAttemptSnapshotBytes {
			return nil
		}
	} else if count >= state.MaxAttemptProgressEvents || size+int64(len(c.Data)) > state.MaxAttemptProgressBytes {
		return nil
	}
	if err := appendEvent(tx, c.RunID, kind, subject, payload); err != nil {
		return err
	}
	if c.Stream == "operation" {
		if ref, ok := payload["operationRef"]; ok {
			raw, err := json.Marshal(ref)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE execution_attempts SET operation_ref_json=? WHERE id=? AND run_id=?`, string(raw), c.AttemptID, c.RunID)
			return err
		}
		return fmt.Errorf("invalid operation reference")
	}
	if c.Snapshot != nil {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO log_chunks(run_id,activation_id,stream,seq,data,timestamp) SELECT ?,?,?,coalesce(max(seq),0)+1,?,? FROM log_chunks WHERE activation_id=? AND stream=?`, c.RunID, c.ActivationID, c.Stream, c.Data, rfc3339(c.Timestamp), c.ActivationID, c.Stream)
	return err
}

// Lease predicates compare TEXT, so all operands must have the same fractional
// width. Keep nanoseconds: SQLite date functions would lose expiry precision.
func leaseTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// Normalize old RFC3339Nano values before any lease predicate can run. Only
// lease timestamps change; unrelated timestamp encodings remain untouched.
func normalizeLeaseTimestamps(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT activation_id,lease_until FROM assignments WHERE lease_until NOT GLOB '????-??-??T??:??:??.?????????Z'`)
	if err != nil {
		return err
	}
	type leaseRow struct{ id, timestamp string }
	var updates []leaseRow
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			rows.Close()
			return fmt.Errorf("assignment %q: %w", id, err)
		}
		updates = append(updates, leaseRow{id, leaseTimestamp(parsed)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range updates {
		if _, err := tx.Exec(`UPDATE assignments SET lease_until=? WHERE activation_id=?`, row.timestamp, row.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
