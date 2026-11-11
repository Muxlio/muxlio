package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/telemetry"
)

func ValidateAttemptOwnership(prior, next ExecutionAttempt) error {
	for _, pair := range [][2]string{{prior.RunID, next.RunID}, {prior.ActivationID, next.ActivationID}, {prior.Executor, next.Executor}, {prior.OperationAttemptID, next.OperationAttemptID}, {prior.OperationAction, next.OperationAction}} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return fmt.Errorf("attempt ownership is immutable")
		}
	}
	if prior.OperationRef != nil && next.OperationRef != nil && (prior.OperationRef.ID != next.OperationRef.ID || prior.OperationRef.URL != next.OperationRef.URL || prior.OperationRef.ContextID != next.OperationRef.ContextID) {
		return fmt.Errorf("attempt remote task identity is immutable")
	}
	return nil
}

func ValidateRemoteAccountingOwnership(query, origin ExecutionAttempt, update contract.RemoteAccountingUpdate) error {
	if err := update.Validate(); err != nil {
		return err
	}
	if query.ID == origin.ID || query.OperationAttemptID != origin.ID || (query.OperationAction != "poll" && query.OperationAction != "reconcile") || query.State != "succeeded" || query.RunID != origin.RunID || query.Executor != origin.Executor {
		return fmt.Errorf("remote accounting lookup is not an authorized owner query")
	}
	if query.OperationRef == nil || origin.OperationRef == nil || query.OperationRef.ID != update.TaskID || origin.OperationRef.ID != update.TaskID || query.OperationRef.URL != origin.OperationRef.URL || query.OperationRef.ContextID != origin.OperationRef.ContextID {
		return fmt.Errorf("remote accounting task identity does not match persisted owner")
	}
	if update.Accounting.Final && query.OperationRef.Pending {
		return fmt.Errorf("remote accounting cannot finalize a pending task")
	}
	return nil
}

func (s *Memory) RecordRemoteAccountingUpdate(queryID string, update contract.RemoteAccountingUpdate) error {
	raw, err := contract.CanonicalAccounting(update.Accounting)
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	s.mu.Lock()
	defer s.mu.Unlock()
	var query, origin *ExecutionAttempt
	for i := range s.attempts {
		if s.attempts[i].ID == queryID {
			query = &s.attempts[i]
			break
		}
	}
	if query == nil {
		return fmt.Errorf("remote accounting query does not exist")
	}
	for i := range s.attempts {
		if s.attempts[i].ID == query.OperationAttemptID {
			origin = &s.attempts[i]
			break
		}
	}
	if origin == nil {
		return fmt.Errorf("remote accounting owner does not exist")
	}
	if err := ValidateRemoteAccountingOwnership(*query, *origin, update); err != nil {
		return err
	}
	owners := 0
	for _, a := range s.attempts {
		if a.OperationAttemptID == "" && a.OperationRef != nil && a.RunID == origin.RunID && a.Executor == origin.Executor && a.OperationRef.ID == origin.OperationRef.ID && a.OperationRef.URL == origin.OperationRef.URL && a.OperationRef.ContextID == origin.OperationRef.ContextID {
			owners++
		}
	}
	if owners != 1 {
		return fmt.Errorf("remote accounting owner is ambiguous")
	}
	if origin.AccountingRemoteRevision == update.SourceRevision {
		if origin.AccountingRemoteHash == hash {
			return nil
		}
		return fmt.Errorf("remote accounting source revision conflict")
	}
	if origin.Accounting != nil {
		if update.Accounting.Revision == origin.Accounting.Revision {
			priorRaw, err := contract.CanonicalAccounting(*origin.Accounting)
			if err != nil {
				return err
			}
			priorHash := fmt.Sprintf("%x", sha256.Sum256(priorRaw))
			if priorHash != hash {
				return fmt.Errorf("remote accounting revision conflict")
			}
			origin.AccountingRemoteRevision = update.SourceRevision
			origin.AccountingRemoteHash = hash
			return nil
		}
		if update.Accounting.Revision < origin.Accounting.Revision {
			return fmt.Errorf("stale remote accounting revision")
		}
		if origin.Accounting.Final {
			return fmt.Errorf("remote accounting owner is already final")
		}
	}
	var copyA contract.Accounting
	if err = json.Unmarshal(raw, &copyA); err != nil {
		return err
	}
	origin.Accounting = &copyA
	origin.AccountingRemoteRevision = update.SourceRevision
	origin.AccountingRemoteHash = hash
	s.appendEventLocked(origin.RunID, EventAccountingUpdated, origin.ActivationID, map[string]any{"attemptId": origin.ID, "revision": update.Accounting.Revision, "hash": hash})
	if update.Accounting.Final {
		telemetry.UsageMetrics(context.Background(), &copyA)
	}
	return nil
}
