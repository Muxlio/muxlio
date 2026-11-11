package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/telemetry"
)

func (s *Store) RecordRemoteAccountingUpdate(queryID string, update contract.RemoteAccountingUpdate) error {
	raw, err := contract.CanonicalAccounting(update.Accounting)
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	read := func(id string) (state.ExecutionAttempt, error) {
		var a state.ExecutionAttempt
		var ref, accounting, rev, h sql.NullString
		err := tx.QueryRow(`SELECT id,run_id,activation_id,executor,state,operation_attempt_id,operation_action,operation_ref_json,accounting_json,remote_accounting_revision,remote_accounting_hash FROM execution_attempts WHERE id=?`, id).Scan(&a.ID, &a.RunID, &a.ActivationID, &a.Executor, &a.State, &a.OperationAttemptID, &a.OperationAction, &ref, &accounting, &rev, &h)
		if err != nil {
			return a, err
		}
		if ref.Valid {
			if err = json.Unmarshal([]byte(ref.String), &a.OperationRef); err != nil {
				return a, err
			}
		}
		if accounting.Valid {
			if err = json.Unmarshal([]byte(accounting.String), &a.Accounting); err != nil {
				return a, err
			}
		}
		a.AccountingRemoteRevision, a.AccountingRemoteHash = rev.String, h.String
		return a, nil
	}
	query, err := read(queryID)
	if err != nil {
		return err
	}
	origin, err := read(query.OperationAttemptID)
	if err != nil {
		return err
	}
	if err = state.ValidateRemoteAccountingOwnership(query, origin, update); err != nil {
		return err
	}
	var owners int
	err = tx.QueryRow(`SELECT COUNT(*) FROM execution_attempts WHERE run_id=? AND executor=? AND operation_attempt_id='' AND json_extract(operation_ref_json,'$.id')=? AND COALESCE(json_extract(operation_ref_json,'$.url'),'')=? AND COALESCE(json_extract(operation_ref_json,'$.contextId'),'')=?`, origin.RunID, origin.Executor, update.TaskID, origin.OperationRef.URL, origin.OperationRef.ContextID).Scan(&owners)
	if err != nil {
		return err
	}
	if owners != 1 {
		return fmt.Errorf("remote accounting owner is ambiguous")
	}
	if origin.AccountingRemoteRevision == update.SourceRevision {
		if origin.AccountingRemoteHash == hash {
			return tx.Commit()
		}
		return fmt.Errorf("remote accounting source revision conflict")
	}
	previous := uint64(0)
	if origin.Accounting != nil {
		previous = origin.Accounting.Revision
		if update.Accounting.Revision == previous {
			priorRaw, err := contract.CanonicalAccounting(*origin.Accounting)
			if err != nil {
				return err
			}
			if fmt.Sprintf("%x", sha256.Sum256(priorRaw)) != hash {
				return fmt.Errorf("remote accounting revision conflict")
			}
			if _, err := tx.Exec(`UPDATE execution_attempts SET remote_accounting_revision=?,remote_accounting_hash=? WHERE id=? AND accounting_revision=?`, update.SourceRevision, hash, origin.ID, previous); err != nil {
				return err
			}
			return tx.Commit()
		}
		if update.Accounting.Revision < previous {
			return fmt.Errorf("stale remote accounting revision")
		}
		if origin.Accounting.Final {
			return fmt.Errorf("remote accounting owner is already final")
		}
	}
	res, err := tx.Exec(`UPDATE execution_attempts SET accounting_json=?,accounting_revision=?,accounting_final=?,accounting_hash=?,remote_accounting_revision=?,remote_accounting_hash=? WHERE id=? AND accounting_revision=? AND accounting_final=0`, string(raw), update.Accounting.Revision, boolInt(update.Accounting.Final), hash, update.SourceRevision, hash, origin.ID, previous)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("remote accounting compare-and-swap failed")
	}
	if err := appendEvent(tx, origin.RunID, state.EventAccountingUpdated, origin.ActivationID, map[string]any{"attemptId": origin.ID, "revision": update.Accounting.Revision, "hash": hash}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if update.Accounting.Final {
		telemetry.UsageMetrics(context.Background(), &update.Accounting)
	}
	return nil
}
