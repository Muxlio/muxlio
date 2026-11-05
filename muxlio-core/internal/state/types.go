package state

import (
	"encoding/json"
	"time"
)

// Run is one invocation of an immutable compiled workflow version.
// PlanJSON, PlanDigest, and Input are fixed at creation; events order transitions.
type Run struct {
	PlanJSON     json.RawMessage `json:"-"`
	PlanDigest   string          `json:"planDigest,omitempty"`
	ID           string          `json:"id"`
	WorkflowName string          `json:"workflowName"`
	State        string          `json:"state"`
	Input        any             `json:"input,omitempty"`
	Output       any             `json:"output,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// Activation is one materialised plan node within a run and structural scope.
type Activation struct {
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
	AssignmentID string    `json:"assignmentId,omitempty"`
	AttemptID    string    `json:"attemptId,omitempty"`
	RunID        string    `json:"runId"`
	ActivationID string    `json:"activationId"`
	WorkerID     string    `json:"workerId"`
	Generation   int64     `json:"generation"`
	LeaseUntil   time.Time `json:"leaseUntil"`
	State        string    `json:"state"`
}

// WorkerCapability is one executor contract advertised by a worker.
type WorkerCapability struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Worker is a registered remote executor and its advertised capabilities.
type Worker struct {
	ID           string             `json:"workerId"`
	InstanceID   string             `json:"instanceId,omitempty"`
	Capacity     int                `json:"capacity"`
	Capabilities []WorkerCapability `json:"capabilities"`
	Labels       map[string]string  `json:"labels,omitempty"`
	LastSeen     time.Time          `json:"lastSeen"`
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

// TokenUsage records the input, output, and cached token counts reported by
// an executor for a single execution attempt. A nil pointer means the provider
// did not report usage; this is intentionally distinct from a reported zero,
// which is represented by a non-nil pointer with all fields set to zero. The
// distinction lets consumers tell "unavailable" from "free/empty" — missing
// provider usage must remain explicitly unavailable, not be reported as zero.
type TokenUsage struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cached int64 `json:"cached"`
}

// ExecutionAttempt records one interpreter call to an executor.
type ExecutionAttempt struct {
	ID                  string      `json:"id"`
	RunID               string      `json:"runId"`
	ActivationID        string      `json:"activationId"`
	RetryNumber         int         `json:"retryNumber"`
	Executor            string      `json:"executor"`
	State               string      `json:"state"`
	StartedAt           time.Time   `json:"startedAt"`
	FinishedAt          time.Time   `json:"finishedAt"`
	Failure             string      `json:"failure,omitempty"`
	Usage               *TokenUsage `json:"usage,omitempty"`
	ExecutionDurationMS *int64      `json:"executionDurationMs,omitempty"`
}

// LogChunk is an append-only chunk of worker output.
type LogChunk struct {
	RunID        string    `json:"runId"`
	ActivationID string    `json:"activationId"`
	Stream       string    `json:"stream"`
	Seq          int       `json:"seq"`
	Data         []byte    `json:"data"`
	Timestamp    time.Time `json:"timestamp"`
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
	EventRunCreated          = "run.created"
	EventRunRunning          = "run.running"
	EventRunWaiting          = "run.waiting"
	EventRunPaused           = "run.paused"
	EventRunSucceeded        = "run.succeeded"
	EventRunFailed           = "run.failed"
	EventRunCancelled        = "run.cancelled"
	EventActivationQueued    = "activation.queued"
	EventActivationRunning   = "activation.running"
	EventActivationWaiting   = "activation.waiting"
	EventActivationSucceeded = "activation.succeeded"
	EventActivationFailed    = "activation.failed"
	EventActivationCancelled = "activation.cancelled"
	EventMapExpanded         = "map.expanded"
	EventBranchSelected      = "branch.selected"
	EventLoopIterated        = "loop.iterated"
	EventWorkerActivity      = "worker.activity"
	EventExecutionAttempt    = "execution.attempt"
)
