package state

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

// Run is one invocation of an immutable compiled workflow version.
// PlanJSON, PlanDigest, and Input are fixed at creation; events order transitions.
// ForkProvenance pins the source and explicitly selected output candidates.
// A fork never inherits attempts, signals, sessions, or external effects.
type ForkProvenance struct {
	SourceRunID      string   `json:"sourceRunId"`
	SourcePlanDigest string   `json:"sourcePlanDigest"`
	ReuseActivations []string `json:"reuseActivations,omitempty"`
}

// SubmissionMetadata is opaque discovery metadata, independent of execution state.
// CallerID is assigned by the authenticated API, never supplied by the client.
type SubmissionMetadata struct {
	OriginLabel string `json:"originLabel,omitempty"`
	CallerID    string `json:"callerId,omitempty"`
}

type Run struct {
	SubmissionMetadata
	WorkspaceID   string          `json:"workspaceId,omitempty"`
	Fork          *ForkProvenance `json:"fork,omitempty"`
	Pinned        bool            `json:"pinned,omitempty"`
	RetainedUntil *time.Time      `json:"retainedUntil,omitempty"`
	PlanJSON      json.RawMessage `json:"-"`
	PlanDigest    string          `json:"planDigest,omitempty"`
	ID            string          `json:"id"`
	WorkflowName  string          `json:"workflowName"`
	State         string          `json:"state"`
	Input         any             `json:"input,omitempty"`
	Output        any             `json:"output,omitempty"`
	Error         string          `json:"error,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// Activation is one materialised plan node within a run and structural scope.
type Activation struct {
	QueueReason string `json:"queueReason,omitempty"`
	// Inputs snapshots resolved bindings before execution. Nil denotes a legacy record.
	Inputs json.RawMessage `json:"inputs,omitempty"`
	// AttemptBase preserves previous attempts when a failed run is explicitly retried.
	AttemptBase int            `json:"attemptBase,omitempty"`
	ID          string         `json:"id"`
	RunID       string         `json:"runId"`
	ScopePath   []string       `json:"scopePath"`
	NodeID      string         `json:"nodeId"`
	Occurrence  int            `json:"occurrence"`
	State       string         `json:"state"`
	Outputs     map[string]any `json:"outputs,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// Event is an append-only record of a run transition or activity.
type Event struct {
	RunID     string         `json:"runId"`
	Seq       int            `json:"seq"`
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	SubjectID string         `json:"subjectId"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// Assignment records a fenced worker attempt. A newer generation invalidates older workers.
type Assignment struct {
	CoordinatorEpoch int64     `json:"coordinatorEpoch"`
	AssignmentID     string    `json:"assignmentId,omitempty"`
	AttemptID        string    `json:"attemptId,omitempty"`
	RunID            string    `json:"runId"`
	ActivationID     string    `json:"activationId"`
	WorkerID         string    `json:"workerId"`
	Generation       int64     `json:"generation"`
	LeaseUntil       time.Time `json:"leaseUntil"`
	State            string    `json:"state"`
}

// AssignmentLease identifies one independently renewed execution slot.
type AssignmentLease struct {
	AssignmentID     string `json:"assignmentId"`
	Generation       int64  `json:"generation"`
	CoordinatorEpoch int64  `json:"coordinatorEpoch"`
}

// WorkerCapability is one executor contract advertised by a worker.
type WorkerCapability struct {
	Readiness *contract.ProbeResult     `json:"readiness,omitempty"`
	Contract  *contract.AdapterManifest `json:"contract,omitempty"`
	Name      string                    `json:"name"`
	Version   string                    `json:"version,omitempty"`
}

// Worker is a registered remote executor and its advertised capabilities.
type Worker struct {
	ID           string             `json:"workerId"`
	InstanceID   string             `json:"instanceId,omitempty"`
	Capacity     int                `json:"capacity"`
	Capabilities []WorkerCapability `json:"capabilities"`
	Labels       map[string]string  `json:"labels,omitempty"`
	LastSeen     time.Time          `json:"lastSeen"`
	// Draining indicates the worker is finishing its current assignments but
	// must not receive any new ones. The coordinator skips draining workers
	// during dispatch. Existing leases continue to be renewed.
	Draining bool `json:"draining,omitempty"`
}

// Dispatch is a durable execute request waiting for a capable worker.
type Dispatch struct {
	AssignmentID string `json:"assignmentId"`
	RunID        string `json:"runId"`
	ActivationID string `json:"activationId"`
	AttemptID    string `json:"attemptId"`
	Executor     string `json:"executor"`
	Request      []byte `json:"request"`
}

// ValueValidationFailure describes a runtime contract violation. Missing is
// distinct from a reported JSON null. Expected is the declared JSON Schema.
type ValueValidationFailure struct {
	Code     string          `json:"code"`
	Path     string          `json:"path"`
	Expected json.RawMessage `json:"expected"`
	Actual   json.RawMessage `json:"actual,omitempty"`
	Missing  bool            `json:"missing,omitempty"`
	Message  string          `json:"message"`
}

func (f *ValueValidationFailure) Error() string {
	actual := string(f.Actual)
	if f.Missing {
		actual = "<missing>"
	}
	return fmt.Sprintf("%s at %s: %s; expected %s; actual %s", f.Code, f.Path, f.Message, f.Expected, actual)
}

// AttemptResult separates executor completion from semantic result validity and
// retains returned ports (including artifact references) on validation failure.
type AttemptResult struct {
	Session            *contract.SessionHandle `json:"session,omitempty"`
	Diagnostic         *diagnostic.Diagnostic  `json:"diagnostic,omitempty"`
	ExecutionSucceeded bool                    `json:"executionSucceeded"`
	Outputs            map[string]any          `json:"outputs,omitempty"`
	ValidationFailure  *ValueValidationFailure `json:"validationFailure,omitempty"`
}

// ExecutionAttempt records one interpreter call to an executor.
// FinishedAt marks the logical attempt outcome. Cancellation does not prove
// executor termination; execution.returned events and worker diagnostics record
// later returns separately, without asserting external effect completion.
type ExecutionAttempt struct {
	// Inherited from the run's immutable workspace selection.
	WorkspaceID              string               `json:"workspaceId,omitempty"`
	OperationAttemptID       string               `json:"operationAttemptId,omitempty"`
	OperationAction          string               `json:"operationAction,omitempty"`
	AccountingRemoteRevision string               `json:"-"`
	AccountingRemoteHash     string               `json:"-"`
	Accounting               *contract.Accounting `json:"accounting,omitempty"`
	ID                       string               `json:"id"`
	RunID                    string               `json:"runId"`
	ActivationID             string               `json:"activationId"`
	RetryNumber              int                  `json:"retryNumber"`
	Executor                 string               `json:"executor"`
	State                    string               `json:"state"`
	StartedAt                time.Time            `json:"startedAt"`
	FinishedAt               time.Time            `json:"finishedAt"`
	Failure                  string               `json:"failure,omitempty"`
	Result                   *AttemptResult       `json:"result,omitempty"`
	ExecutionDurationMS      *int64               `json:"executionDurationMs,omitempty"`

	// Effect is the declared policy: pure, idempotent-with-key, reconcilable,
	// nonretryable, or a legacy alias. It is
	// stored so operators can determine the safety classification of any
	// attempt whose state requires reconciliation.
	Effect string `json:"effect,omitempty"`

	// IdempotencyKey is the stable key associated with this attempt under the
	// "idempotency-key" effect policy. A non-empty key means the coordinator
	// passes it unchanged to a capable adapter for external deduplication.
	// A stored key alone does not establish completion.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`

	// OperationRef is the external provider-assigned reference returned by the
	// executor for this attempt (e.g. a remote job ID or task URL). Recorded
	// when the executor returns a RemoteTask reference so that adapter-supplied
	// reconciliation can locate and query the operation after a worker crash.
	// Nil means the executor did not return an operation reference.
	OperationRef *contract.RemoteTaskReference `json:"operationRef,omitempty"`

	// AwaitReconciliationReason is set when this attempt is held for explicit
	// operator resolution because the external effect cannot be automatically
	// reconciled. A non-empty value means re-dispatch is blocked and the reason
	// is surfaced verbatim to the operator so they know why the attempt is
	// pending reconciliation.
	AwaitReconciliationReason string `json:"awaitReconciliationReason,omitempty"`
}

// LogChunk is an append-only chunk of worker output.
type LogChunk struct {
	AssignmentID     string            `json:"assignmentId,omitempty"`
	WorkerID         string            `json:"workerId,omitempty"`
	Generation       int64             `json:"generation,omitempty"`
	CoordinatorEpoch int64             `json:"coordinatorEpoch,omitempty"`
	AttemptID        string            `json:"attemptId,omitempty"`
	Snapshot         *ArtifactSnapshot `json:"snapshot,omitempty"`
	RunID            string            `json:"runId"`
	ActivationID     string            `json:"activationId"`
	Stream           string            `json:"stream"`
	Seq              int               `json:"seq"`
	Data             []byte            `json:"data"`
	Timestamp        time.Time         `json:"timestamp"`
}

// ArtifactSnapshot is an immutable observation, never a committed named output.
// Run/activation/attempt provenance and sequence are carried by its enclosing event.
type ArtifactSnapshot struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Mime   string `json:"mime,omitempty"`
	Size   int64  `json:"size"`
}

const EventProgress = "execution.progress"
const EventArtifactSnapshot = "artifact.snapshot"
const MaxAttemptProgressEvents = 256
const MaxAttemptProgressBytes = 1 << 20
const MaxAttemptSnapshots = 128
const MaxAttemptSnapshotBytes = 64 << 20

// ProgressEvent maps worker observations into the shared, replayable event log.
func ProgressEvent(c LogChunk) (string, string, map[string]any) {
	subject := c.AttemptID
	if subject == "" {
		subject = c.ActivationID
	}
	payload := map[string]any{"activationId": c.ActivationID, "attemptId": c.AttemptID, "stream": c.Stream, "sourceSeq": c.Seq}
	if c.AssignmentID != "" {
		payload["assignmentId"], payload["workerId"] = c.AssignmentID, c.WorkerID
		payload["generation"], payload["coordinatorEpoch"] = c.Generation, c.CoordinatorEpoch
	}
	if c.Snapshot != nil {
		payload["snapshot"] = *c.Snapshot
		return EventArtifactSnapshot, subject, payload
	}
	if c.Stream == "operation" {
		var ref contract.RemoteTaskReference
		if json.Unmarshal(c.Data, &ref) == nil && ref.ID != "" {
			payload["operationRef"] = ref
		}
		return "execution.operation", subject, payload
	}
	payload["text"] = string(c.Data)
	return EventProgress, subject, payload
}

// Timer is a persisted deadline/wakeup for an activation.
type Timer struct {
	ID           string
	RunID        string
	ActivationID string
	DueAt        time.Time
	State        string // pending, fired, cancelled
	Payload      map[string]any
}

// Signal is a typed external message delivered to an explicitly waiting
// workflow node.
type Signal struct {
	RunID          string
	Name           string
	Seq            int
	Payload        map[string]any
	Timestamp      time.Time
	IdempotencyKey string // optional client-supplied deduplication key
}

// InputRequest is a typed, durable request for external input, identified by a
// stable RequestID. An approval is one example of a generic input request, not
// a distinct run state: the run waits (without consuming a worker slot) until a
// response is delivered or the request expires. Each request carries an
// optional JSON Schema for validating responses, an optional expiration
// deadline, and the permissions a responder must hold. Responses are delivered
// at most once: a second submission with the same IdempotencyKey returns the
// already-accepted response without re-applying it.
type InputRequest struct {
	RunID        string          `json:"runId"`
	ActivationID string          `json:"activationId"`
	RequestID    string          `json:"requestId"`
	Name         string          `json:"name"`                  // the signal name the wait node expects
	Schema       json.RawMessage `json:"schema,omitempty"`      // optional JSON Schema for response validation
	ExpiresAt    *time.Time      `json:"expiresAt,omitempty"`   // optional expiration deadline
	Permissions  []string        `json:"permissions,omitempty"` // required responder permissions
	State        string          `json:"state"`                 // pending, fulfilled, expired, cancelled
	Response     any             `json:"response,omitempty"`    // the accepted JSON response payload
	ResponderID  string          `json:"responderId,omitempty"` // who submitted the accepted response
	CreatedAt    time.Time       `json:"createdAt"`
	FulfilledAt  *time.Time      `json:"fulfilledAt,omitempty"`
	// IdempotencyKey is the dedup key of the accepted response. A second
	// submission with the same key is a no-op that returns this request.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// Input request states.
const (
	InputRequestPending   = "pending"
	InputRequestFulfilled = "fulfilled"
	InputRequestExpired   = "expired"
	InputRequestCancelled = "cancelled"
)

// OutboxRecord is an append-only, at-least-once message queued for external delivery or worker notifications.
type OutboxRecord struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Payload     any        `json:"payload,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

// Event types.
const (
	EventInputRequired          = "input.required"
	EventReconciliationRequired = "execution.reconciliation_required"
	EventRunCreated             = "run.created"
	EventRunRunning             = "run.running"
	EventRunWaiting             = "run.waiting"
	EventRunPaused              = "run.paused"
	EventRunSucceeded           = "run.succeeded"
	EventRunFailed              = "run.failed"
	EventRunCancelled           = "run.cancelled"
	EventActivationQueued       = "activation.queued"
	EventActivationRunning      = "activation.running"
	EventActivationWaiting      = "activation.waiting"
	EventActivationSucceeded    = "activation.succeeded"
	EventActivationFailed       = "activation.failed"
	EventActivationCancelled    = "activation.cancelled"
	EventMapExpanded            = "map.expanded"
	EventBranchSelected         = "branch.selected"
	EventLoopIterated           = "loop.iterated"
	EventWorkerActivity         = "worker.activity"
	EventExecutionAttempt       = "execution.attempt"
	EventAccountingUpdated      = "execution.accounting.updated"
)

// AttemptDiagnostics returns the shared diagnostics from failed execution attempts.
func AttemptDiagnostics(attempts []ExecutionAttempt) []diagnostic.Diagnostic {
	result := make([]diagnostic.Diagnostic, 0)
	for _, attempt := range attempts {
		if attempt.Result != nil && attempt.Result.Diagnostic != nil {
			result = append(result, *attempt.Result.Diagnostic)
		}
		if len(result) == diagnostic.MaxIssues {
			break
		}
	}
	// Bound the collection as well as each individual diagnostic.
	raw, err := diagnostic.JSON(result)
	if err != nil {
		return nil
	}
	var bounded []diagnostic.Diagnostic
	if contract.DecodeJSON(raw, &bounded) != nil {
		return nil
	}
	return bounded
}
