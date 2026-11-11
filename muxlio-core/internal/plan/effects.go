package plan

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// reconcileEffect holds uncertainty durably before asking an adapter. A missing
// or inconclusive answer never authorizes another external operation.
func (e *Engine) reconcileEffect(req workers.ExecuteRequest, a state.ExecutionAttempt, node *Node) (workers.ExecuteResult, bool, error) {
	a.State = "awaiting_reconciliation"
	a.AwaitReconciliationReason = awaitReconciliationReason(a.Effect, node.ID, a.ID)
	if a.AwaitReconciliationReason == "" {
		a.AwaitReconciliationReason = "external outcome unknown; explicit resolution required"
	}
	// A reference may have arrived on the progress channel before worker loss.
	prior, err := e.store.ListExecutionAttempts(a.RunID, a.ActivationID)
	if err != nil {
		return workers.ExecuteResult{}, false, err
	}
	for _, old := range prior {
		if old.ID == a.ID && a.OperationRef == nil {
			a.OperationRef = old.OperationRef
		}
	}
	if err := e.store.RecordExecutionAttempt(a); err != nil {
		return workers.ExecuteResult{}, false, err
	}
	if req.Session != nil || (a.Effect != "reconcile" && a.Effect != "reconcilable") {
		return workers.ExecuteResult{}, false, workers.UncertainExecutionError{}
	}
	req.Action = "reconcile"
	req.OperationAttemptID = a.ID
	req.RemoteTask = a.OperationRef
	req.ActivationID = a.ID + "-reconciliation"
	// Each outcome lookup is an invocation too; use a fresh durable identity.
	priorQueries, err := e.store.ListExecutionAttempts(a.RunID, req.ActivationID)
	if err != nil {
		return workers.ExecuteResult{}, false, err
	}
	req.AttemptID = fmt.Sprintf("%s-%d", req.ActivationID, len(priorQueries)+1)
	req.Effect = "pure"
	// Outcome lookup must not wait for exclusive resources held by the lost
	// operation. The adapter's reconciliation capability is read-only and
	// must not reopen the operation's writable workspace.
	req.ConcurrencyKeys = nil
	req.WorkspaceMode = ""
	req.Workspace = ""
	if err := narrowLookupPolicy(&req); err != nil {
		return workers.ExecuteResult{}, false, err
	}
	labels := make(map[string]string, len(req.PlacementLabels))
	for key, value := range req.PlacementLabels {
		if key != workers.WorkspacePlacementLabel {
			labels[key] = value
		}
	}
	req.PlacementLabels = labels
	req.ExecutionDeadline, req.QueueDeadline = nil, nil
	req.Context = e.telemetryContext()
	req.OnExecutionReturned = nil
	// A reconciliation query is bounded and read-only. It must not silently start work.
	seconds := 30.0
	req.TimeoutSeconds, req.QueueTimeoutSeconds = &seconds, &seconds
	query := state.ExecutionAttempt{ID: req.AttemptID, RunID: a.RunID, ActivationID: req.ActivationID, Executor: req.Executor, RetryNumber: len(priorQueries) + 1, State: "running", StartedAt: time.Now(), Effect: "pure"}
	query.OperationAttemptID, query.OperationAction, query.OperationRef = a.ID, "reconcile", a.OperationRef
	if err := e.admitAttempt(query); err != nil {
		return workers.ExecuteResult{}, false, err
	}
	req.LogSink = nil
	req.AccountingSink = attemptAccountingSink{record: func(snapshot contract.Accounting) error {
		return e.store.RecordAccountingSnapshot(query.ID, snapshot)
	}}
	result, err := workers.ExecuteAttempt(e.executor, req)
	query.State, query.FinishedAt = "succeeded", time.Now()
	query.Accounting = result.Accounting
	if result.RemoteTask != nil {
		query.OperationRef = result.RemoteTask
	}
	query.ExecutionDurationMS = result.ExecutionDurationMS
	if err != nil {
		query.State, query.Failure = "failed", diagnostic.Redact(workers.RedactSecrets(err.Error(), req.Secrets))
		var uncertain workers.UncertainExecutionError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &uncertain) {
			// A read-only query can still consume resources after local waiting
			// stops. Keep its active slot until termination is explicitly resolved.
			query.State = "awaiting_reconciliation"
			query.AwaitReconciliationReason = "reconciliation query completion unknown; explicit resolution required"
		}
	} else if result.Error != "" && !remoteTerminalFailure(result) {
		query.State, query.Failure = "failed", diagnostic.Redact(workers.RedactSecrets(result.Error, req.Secrets))
	}
	if recordErr := e.store.RecordExecutionAttempt(query); recordErr != nil {
		return workers.ExecuteResult{}, false, recordErr
	}
	if query.State == "succeeded" && result.RemoteAccounting != nil {
		if updateErr := e.store.RecordRemoteAccountingUpdate(query.ID, *result.RemoteAccounting); updateErr != nil {
			_ = e.store.AppendEvent(a.RunID, "execution.accounting.anomaly", a.ID, map[string]any{"reason": "remote-accounting-attribution-rejected"})
		}
	}
	result.Accounting, result.RemoteAccounting = nil, nil
	result.ExecutionDurationMS = a.ExecutionDurationMS
	if err == nil && remoteTerminalFailure(result) && result.Reconciliation != nil && result.Reconciliation.Status == "completed" {
		a.State, a.FinishedAt = "failed", time.Now()
		a.Failure = diagnostic.Redact(workers.RedactSecrets(result.Error, req.Secrets))
		a.OperationRef, a.AwaitReconciliationReason = result.RemoteTask, ""
		if err := e.store.RecordExecutionAttempt(a); err != nil {
			return workers.ExecuteResult{}, false, err
		}
		// The lookup succeeded and confirmed an unsuccessful operation.
		// Return the terminal result through the normal execution failure path.
		return result, false, nil
	}
	if err == nil && result.Error == "" && result.Reconciliation != nil {
		status := result.Reconciliation.Status
		if status == "pending" && remotePending(result) {
			return result, false, nil
		}
		if status == "completed" || status == "not-applied" {
			resolution := state.AttemptResolution{Outcome: status, Reason: diagnostic.Redact(workers.RedactSecrets(result.Reconciliation.Reason, req.Secrets))}
			if resolution.Reason == "" {
				resolution.Reason = "adapter confirmed " + status
			}
			if status == "completed" {
				if err := ValidateExecuteResult(node.Outputs, result); err != nil {
					a.AwaitReconciliationReason += "; reconciled result is invalid: " + err.Error()
				} else {
					resolution.Result = &state.AttemptResult{ExecutionSucceeded: true, Outputs: result.Outputs}
				}
			}
			if status == "not-applied" || resolution.Result != nil {
				if err := e.store.ResolveExecutionAttempt(a.RunID, a.ID, resolution); err != nil {
					return workers.ExecuteResult{}, false, err
				}
				return result, status == "not-applied", nil
			}
		} else if result.Reconciliation.Reason != "" {
			a.AwaitReconciliationReason += "; " + result.Reconciliation.Reason
		}
	}
	if err != nil {
		a.AwaitReconciliationReason += "; reconciliation failed: " + err.Error()
	} else if result.Error != "" && !remoteTerminalFailure(result) {
		a.AwaitReconciliationReason += "; reconciliation failed: " + result.Error
	}
	a.AwaitReconciliationReason = workers.RedactSecrets(a.AwaitReconciliationReason, req.Secrets)
	a.FinishedAt = time.Now()
	if err := e.store.RecordExecutionAttempt(a); err != nil {
		return workers.ExecuteResult{}, false, err
	}
	return workers.ExecuteResult{}, false, workers.UncertainExecutionError{}
}
