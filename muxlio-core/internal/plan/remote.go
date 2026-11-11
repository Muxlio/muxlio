package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// An error with a completed remote reference describes the operation outcome;
// lookup failures omit that terminal reference and remain retryable reads.
func remoteTerminalFailure(result workers.ExecuteResult) bool {
	return result.Error != "" && result.RemoteTask != nil && result.RemoteTask.ID != "" && !result.RemoteTask.Pending
}

func remotePending(result workers.ExecuteResult) bool {
	return result.RemoteTask != nil && result.RemoteTask.Pending
}

// pollRemote checkpoints the handoff before waiting. Waiting owns no executor
// invocation, worker lease, subprocess, workspace, or concurrency reservation.
// Each poll is a bounded, read-only invocation with a separate durable identity.
func (e *Engine) pollRemote(req workers.ExecuteRequest, a state.ExecutionAttempt, node *Node) (workers.ExecuteResult, error) {
	if req.RequiredCapabilities[contract.CapabilityRemoteTask] == "" || req.Session != nil || req.SessionID != "" || a.OperationRef == nil || a.OperationRef.ID == "" {
		a.State = "awaiting_reconciliation"
		a.AwaitReconciliationReason = "invalid remote handoff; remoteTask capability and remote identity required; local sessions cannot detach"
		if err := e.store.RecordExecutionAttempt(a); err != nil {
			return workers.ExecuteResult{}, err
		}
		return workers.ExecuteResult{}, workers.UncertainExecutionError{}
	}
	for {
		ref := *a.OperationRef
		if ref.PollAt.IsZero() {
			ref.PollAt = time.Now().Add(30 * time.Second).UTC()
		}
		a.OperationRef = &ref
		a.State, a.AwaitReconciliationReason = "detached", ""
		if err := e.store.RecordExecutionAttempt(a); err != nil {
			return workers.ExecuteResult{}, err
		}
		if err := e.wait(time.Until(ref.PollAt)); err != nil {
			return workers.ExecuteResult{}, err
		}
		if err := e.checkpoint(); err != nil {
			return workers.ExecuteResult{}, err
		}
		queryReq := req
		queryReq.Action, queryReq.OperationAttemptID, queryReq.RemoteTask = "poll", a.ID, &ref
		queryReq.ActivationID = a.ID + "-poll"
		prior, err := e.store.ListExecutionAttempts(a.RunID, queryReq.ActivationID)
		if err != nil {
			return workers.ExecuteResult{}, err
		}
		queryReq.AttemptID = fmt.Sprintf("%s-%d", queryReq.ActivationID, len(prior)+1)
		// A queued poll may already have completed immediately before restart.
		if _, queued := e.executor.(workers.QueuedExecutor); queued && len(prior) > 0 && prior[len(prior)-1].State == "running" {
			queryReq.AttemptID = prior[len(prior)-1].ID
		}
		queryReq.Context, queryReq.Effect = e.telemetryContext(), "pure"
		queryReq.Workspace, queryReq.WorkspaceMode, queryReq.ConcurrencyKeys = "", "", nil
		// Polls run in scratch even when submission required a host workspace.
		// Narrow that filesystem grant in both representations of the policy;
		// otherwise worker validation rejects every poll after workspace removal.
		if err := narrowLookupPolicy(&queryReq); err != nil {
			return workers.ExecuteResult{}, err
		}
		queryReq.PlacementLabels = make(map[string]string)
		for key, value := range req.PlacementLabels {
			if key != workers.WorkspacePlacementLabel {
				queryReq.PlacementLabels[key] = value
			}
		}
		queryReq.ExecutionDeadline, queryReq.QueueDeadline, queryReq.Deadline = nil, nil, nil
		queryReq.OnExecutionReturned, queryReq.LogSink = nil, nil
		seconds := 30.0
		queryReq.TimeoutSeconds, queryReq.QueueTimeoutSeconds = &seconds, &seconds
		query := state.ExecutionAttempt{ID: queryReq.AttemptID, RunID: a.RunID, ActivationID: queryReq.ActivationID, Executor: req.Executor, RetryNumber: len(prior) + 1, State: "running", Effect: "pure", StartedAt: time.Now()}
		query.OperationAttemptID, query.OperationAction, query.OperationRef = a.ID, "poll", a.OperationRef
		if err := e.admitAttempt(query); err != nil {
			return workers.ExecuteResult{}, err
		}
		queryReq.AccountingSink = attemptAccountingSink{record: func(snapshot contract.Accounting) error {
			return e.store.RecordAccountingSnapshot(query.ID, snapshot)
		}}
		result, err := workers.ExecuteAttempt(e.executor, queryReq)
		if e.context().Err() != nil {
			return workers.ExecuteResult{}, e.context().Err()
		}
		query.State, query.FinishedAt = "succeeded", time.Now()
		query.Accounting, query.ExecutionDurationMS = result.Accounting, result.ExecutionDurationMS
		if result.RemoteTask != nil {
			query.OperationRef = result.RemoteTask
		}
		if err != nil || (result.Error != "" && !remoteTerminalFailure(result)) {
			query.State = "failed"
			query.Failure = "remote poll failed; remote operation remains detached"
			var uncertain workers.UncertainExecutionError
			if errors.As(err, &uncertain) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				query.State = "awaiting_reconciliation"
				query.AwaitReconciliationReason = "poll invocation termination unknown; remote operation remains detached"
			}
		}
		if recordErr := e.store.RecordExecutionAttempt(query); recordErr != nil {
			return workers.ExecuteResult{}, recordErr
		}
		if query.State == "succeeded" && result.RemoteAccounting != nil {
			if updateErr := e.store.RecordRemoteAccountingUpdate(query.ID, *result.RemoteAccounting); updateErr != nil {
				_ = e.store.AppendEvent(a.RunID, "execution.accounting.anomaly", a.ID, map[string]any{"reason": "remote-accounting-attribution-rejected"})
			}
		}
		// Lookup consumption has already been stored on query. Returning it as
		// the operation result would overwrite the origin's remote accounting.
		result.Accounting, result.RemoteAccounting = nil, nil
		result.ExecutionDurationMS = a.ExecutionDurationMS
		if query.State != "succeeded" {
			// A failed lookup says nothing about the remote operation. Retry
			// only the lookup, never the submission.
			ref.PollAt = time.Time{}
			a.OperationRef = &ref
			continue
		}
		if remoteTerminalFailure(result) {
			if result.RemoteTask.ID != ref.ID {
				return workers.ExecuteResult{}, fmt.Errorf("remote poll changed task identity")
			}
			a.State, a.FinishedAt, a.Failure = "failed", time.Now(), diagnostic.Redact(workers.RedactSecrets(result.Error, req.Secrets))
			a.OperationRef = result.RemoteTask
			if err := e.store.RecordExecutionAttempt(a); err != nil {
				return workers.ExecuteResult{}, err
			}
			return result, fmt.Errorf("remote operation failed: %s", a.Failure)
		}
		if remotePending(result) {
			if result.RemoteTask.ID != ref.ID {
				return workers.ExecuteResult{}, fmt.Errorf("remote poll changed task identity")
			}
			a.OperationRef = result.RemoteTask
			continue
		}
		if err := ValidateExecuteResult(node.Outputs, result); err != nil {
			return workers.ExecuteResult{}, err
		}
		a.State, a.FinishedAt = "succeeded", time.Now()
		a.Result = &state.AttemptResult{ExecutionSucceeded: true, Outputs: result.Outputs}
		if err := e.store.RecordExecutionAttempt(a); err != nil {
			return workers.ExecuteResult{}, err
		}
		if e.plan.Limits != nil {
			attempts, err := e.store.ListExecutionAttempts(a.RunID, "")
			if err != nil {
				return workers.ExecuteResult{}, err
			}
			if err := e.checkAccounting(attempts, false); err != nil {
				return workers.ExecuteResult{}, err
			}
		}
		return result, nil
	}
}

// narrowLookupPolicy removes workspace grants from read-only outcome lookups.
// Keep the typed policy and its serialized representation consistent without
// mutating the submission request's policy or parameters.
func narrowLookupPolicy(req *workers.ExecuteRequest) error {
	var err error
	if req.Policy != nil && (req.Policy.Filesystem == "workspace-read-only" || req.Policy.Filesystem == "workspace-write") {
		policy := *req.Policy
		policy.Filesystem = "scratch"
		req.Policy = &policy
		if len(req.Params) > 0 {
			var params map[string]json.RawMessage
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return err
			}
			if _, present := params["executionPolicy"]; present {
				params["executionPolicy"], err = json.Marshal(policy)
				if err != nil {
					return err
				}
				req.Params, err = json.Marshal(params)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}
