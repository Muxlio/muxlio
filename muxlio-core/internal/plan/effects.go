package plan

import (
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
	"time"
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
	if a.Effect != "reconcile" && a.Effect != "reconcilable" {
		return workers.ExecuteResult{}, false, workers.UncertainExecutionError{}
	}
	req.Action = "reconcile"
	req.OperationAttemptID = a.ID
	req.RemoteTask = a.OperationRef
	req.ActivationID = a.ID + "-reconciliation"
	req.AttemptID = req.ActivationID
	req.Effect = "pure"
	// Outcome lookup must not wait for exclusive resources held by the lost
	// operation. The adapter's reconciliation capability is read-only and
	// must not reopen the operation's writable workspace.
	req.ConcurrencyKeys = nil
	req.WorkspaceMode = ""
	req.Workspace = ""
	labels := make(map[string]string, len(req.PlacementLabels))
	for key, value := range req.PlacementLabels {
		if key != workers.WorkspacePlacementLabel {
			labels[key] = value
		}
	}
	req.PlacementLabels = labels
	req.ExecutionDeadline, req.QueueDeadline = nil, nil
	req.Context = e.context()
	req.OnExecutionReturned = nil
	// A reconciliation query is bounded and read-only. It must not silently start work.
	seconds := 30.0
	req.TimeoutSeconds, req.QueueTimeoutSeconds = &seconds, &seconds
	result, err := workers.ExecuteAttempt(e.executor, req)
	if err == nil && result.Error == "" && result.Reconciliation != nil {
		status := result.Reconciliation.Status
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
	} else if result.Error != "" {
		a.AwaitReconciliationReason += "; reconciliation failed: " + result.Error
	}
	a.AwaitReconciliationReason = workers.RedactSecrets(a.AwaitReconciliationReason, req.Secrets)
	a.FinishedAt = time.Now()
	if err := e.store.RecordExecutionAttempt(a); err != nil {
		return workers.ExecuteResult{}, false, err
	}
	return workers.ExecuteResult{}, false, workers.UncertainExecutionError{}
}
