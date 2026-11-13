package state

import (
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-adapters/contract"
	"time"
)

// ErrInputRequestNotAccepting is returned by SubmitInputResponse when the
// request cannot accept the submission: it is already fulfilled with a
// different idempotency key, or it has expired or been cancelled.
var ErrInputRequestNotAccepting = errors.New("muxlio: input request is not accepting responses")

// ErrIdempotencyKeyConflict is returned by CreateRunIdempotent when the key
// was already used with a different plan digest, input, or workspace identity.
var ErrIdempotencyKeyConflict = errors.New("muxlio: idempotency key reused with different payload")

// ErrWorkflowVersionRetired prevents new submissions of a retired definition.
var ErrWorkflowVersionRetired = errors.New("muxlio: workflow version is retired")

// WorkflowVersionSummary provides metadata for a registered immutable workflow version.
type WorkflowVersionSummary struct {
	Retired   bool   `json:"retired,omitempty"`
	Digest    string `json:"digest"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

// Store is the coordinator's current-state and event log.
// Memory and SQLite implementations share this contract so a run can
// continue after the process that created it has exited.
type Store interface {
	SessionQueueStore
	CreateRun(run *Run) error
	// CreateRunIdempotent creates a run bound to idempotencyKey, planDigest,
	// inputDigest and workspaceID.  When a run with the same key already exists
	// and all binding fields match, the existing run ID is returned (Duplicate=true).
	// When the key exists but the bindings differ, ErrIdempotencyKeyConflict is
	// returned.  An empty key falls back to plain CreateRun.
	CreateRunIdempotent(run *Run, idempotencyKey, inputDigest string) (runID string, duplicate bool, err error)
	GetRun(id string) (*Run, error)
	SetRunState(id, state string) error
	TransitionRunState(id string, expected []string, next string) (bool, error)
	// RetryRun atomically requeues incomplete work and renews cancelled input
	// waits under new identities. Fulfilled responses and closed identities remain.
	RetryRun(id string) error
	SetRunOutput(id string, output any) error
	SetRunError(id string, err error) error
	// UpdateRunPlan only accepts the existing pin; changing a version requires a new run.
	UpdateRunPlan(id string, planJSON json.RawMessage, digest string) error
	ListRuns() ([]*Run, error)

	CreateActivation(a *Activation) error
	GetActivation(id string) (*Activation, error)
	ListActivations(runID string) ([]Activation, error)
	SetActivationState(id, state string) error
	SetActivationOutputs(id string, outputs map[string]any) error
	// CheckpointActivation atomically saves outputs and marks the activation
	// waiting, without publishing a successful completion. Terminal activations
	// are unchanged; a cancelled run rejects the write.
	CheckpointActivation(id string, outputs map[string]any) error
	SetActivationError(id string, err error) error

	GetEvents(runID string, after, limit int) ([]Event, error)
	AppendEvent(runID, eventType, subjectID string, payload map[string]any) error

	// Timers: durable deadline/wakeup for an activation.
	ScheduleTimer(id, runID, activationID string, due time.Time, payload any) error
	GetTimer(id string) (*Timer, error)
	DueTimers(now time.Time) ([]Timer, error)
	CancelTimer(id string) error

	// Signals: typed external messages delivered to waiting nodes.
	// SendSignal delivers a signal to a run. If idempotencyKey is non-empty,
	// a second call with the same key is a no-op (the first delivery is returned).
	SendSignal(runID, name string, payload any, idempotencyKey ...string) error
	ConsumeSignals(runID, name string) ([]Signal, error)

	// Input requests: typed durable requests for external input. Each request
	// has a stable request ID, an optional JSON Schema for validating responses,
	// an optional expiration, and required responder permissions. Responses are
	// delivered at most once via an idempotency key.
	//
	// CreateInputRequest is idempotent: a second call for the same run+requestID
	// is a no-op, so the interpreter can (re)create a request on run retry without
	// losing an already-accepted response. A new pending request created after
	// durable run cancellation must be persisted as cancelled for retry renewal.
	CreateInputRequest(req *InputRequest) error
	GetInputRequest(runID, requestID string) (*InputRequest, error)
	ListInputRequests(runID string) ([]InputRequest, error)
	// SubmitInputResponse delivers a response at most once. It returns the
	// updated request and a boolean: true when this submission newly fulfilled
	// the request, false when it was an idempotent replay of an already-accepted
	// response (same idempotency key). A request that is already fulfilled with
	// a different key, or expired/cancelled, returns ErrInputRequestNotAccepting.
	// First acceptance must be atomic with durable run cancellation and reject
	// a cancelled run. Replay of responses accepted before cancellation remains valid.
	SubmitInputResponse(runID, requestID string, response any, responderID, idempotencyKey string) (*InputRequest, bool, error)
	// ExpireInputRequests marks pending requests past now as expired and
	// returns the number transitioned. The interpreter uses it to fail waits
	// whose input deadline elapsed without a response.
	ExpireInputRequests(now time.Time) (int, error)

	// Workflow versions: immutable compiled Plan IR registry.
	SaveWorkflowVersion(digest string, planJSON []byte) error
	GetWorkflowVersion(digest string) ([]byte, error)
	RetireWorkflowVersion(digest string) error
	ListWorkflowVersions() ([]WorkflowVersionSummary, error)

	// Chunked logs: bounded append-only worker output.
	AppendLogChunk(c LogChunk) error
	GetLogChunks(runID, activationID string) ([]LogChunk, error)
	ListAssignments(runID string) ([]Assignment, error)
	CancelRunWork(runID string) error
	RecordExecutionAttempt(a ExecutionAttempt) error
	RecordAccountingSnapshot(attemptID string, snapshot contract.Accounting) error
	RecordRemoteAccountingUpdate(queryAttemptID string, update contract.RemoteAccountingUpdate) error
	ListExecutionAttempts(runID, activationID string) ([]ExecutionAttempt, error)
	ResolveExecutionAttempt(runID, attemptID string, resolution AttemptResolution) error

	// Assignments: fenced worker attempts.
	ClaimAssignment(a Assignment) (int64, error)
	CompleteAssignment(activationID string, generation int64) (bool, error)

	// Outbox: reliable, at-least-once messaging queue.
	// EnqueueOutbox persists an unpublished record. For atomic coordination,
	// callers can enqueue messages as part of state-changing operations.
	// ListUnpublishedOutbox and MarkOutboxPublished provide the poll-mark-ack cycle.
	EnqueueOutbox(id, kind string, payload any) error
	ListUnpublishedOutbox(limit int) ([]OutboxRecord, error)
	MarkOutboxPublished(id string, publishedAt time.Time) error

	Close() error
}

// EventTypeForRunState returns the history event for a run state, or empty.
func EventTypeForRunState(state string) string {
	switch state {
	case "running":
		return EventRunRunning
	case "waiting":
		return EventRunWaiting
	case "paused":
		return EventRunPaused
	case "succeeded":
		return EventRunSucceeded
	case "failed":
		return EventRunFailed
	case "cancelled":
		return EventRunCancelled
	default:
		return ""
	}
}

// EventTypeForActivationState returns the history event for an activation state, or empty.
func EventTypeForActivationState(state string) string {
	switch state {
	case "running":
		return EventActivationRunning
	case "waiting":
		return EventActivationWaiting
	case "succeeded":
		return EventActivationSucceeded
	case "failed":
		return EventActivationFailed
	case "cancelled":
		return EventActivationCancelled
	default:
		return ""
	}
}

func outputKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
