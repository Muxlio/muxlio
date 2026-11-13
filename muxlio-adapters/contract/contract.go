// Package contract defines the provider-independent executor protocol. Start is
// one JSON request on stdin, progress is NDJSON on MUXLIO_LOG_FD, and completion
// is exactly one JSON Result on stdout. Stderr is diagnostic output, never a
// result. Process termination is the baseline cancellation mechanism; cancel
// capability means graceful cancellation. Optional features are opt-in.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

const Version = "1"

type OutputDecl struct {
	Mime   string          `json:"mime,omitempty"`
	Path   string          `json:"path,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// RemoteTaskReference is independent of provider task states. A non-pending
// reference accompanying Result.Error confirms terminal operation failure.
type RemoteTaskReference struct {
	// ContextID retains the remote conversation context independently of local sessions.
	ContextID string `json:"contextId,omitempty"`
	ID        string `json:"id"`
	URL       string `json:"url,omitempty"`
	// Pending hands off a remote operation after the adapter process exits.
	// PollAt schedules a read-only poll; zero uses the coordinator's interval.
	Pending bool      `json:"pending,omitempty"`
	PollAt  time.Time `json:"pollAt,omitempty"`
}

// Reconciliation is supplied by the adapter after a read-only outcome lookup.
// not-applied promises the old operation cannot later take effect.
type Reconciliation struct {
	Status string `json:"status"` // completed, not-applied, inconclusive, pending (with RemoteTask)
	Reason string `json:"reason,omitempty"`
}

type Result struct {
	RemoteAccounting   *RemoteAccountingUpdate `json:"remoteAccounting,omitempty"`
	AcknowledgedInputs []string                `json:"acknowledgedInputs,omitempty"`
	Session            *SessionHandle          `json:"session,omitempty"`
	Accounting         *Accounting             `json:"accounting,omitempty"`
	Reconciliation     *Reconciliation         `json:"reconciliation,omitempty"`
	// OutputErrors records invalid or unreadable JSON ports independently of
	// executor completion. Outputs may retain rejected text for diagnostics.
	OutputErrors map[string]string `json:"outputErrors,omitempty"`

	Outputs   map[string]any `json:"outputs"`
	Error     string         `json:"error"`
	Logs      string         `json:"logs,omitempty"`
	SessionID string         `json:"sessionId,omitempty"`
	// A non-pending reference alongside Error confirms a terminal remote
	// operation failure. Lookup errors must not claim a terminal reference.
	RemoteTask          *RemoteTaskReference `json:"remoteTask,omitempty"`
	ExecutionDurationMS *int64               `json:"executionDurationMs,omitempty"`
}

// ParseResult accepts exactly one result object. A JSON null is not a completed
// invocation, and invalid reported measurements must not become valid usage.
// Empty objects remain valid for executors with no outputs or usage to report.
func ParseResult(raw []byte) (Result, error) {
	var result Result
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		for _, legacy := range []string{"usage", "measurements"} {
			if _, ok := fields[legacy]; ok {
				return result, fmt.Errorf("executor result field %s was removed; emit accounting", legacy)
			}
		}
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return result, fmt.Errorf("executor result must be a JSON object")
	}
	if err := DecodeJSON(raw, &result); err != nil {
		return Result{}, fmt.Errorf("executor result: %w", err)
	}
	if result.Session != nil {
		if err := result.Session.Validate(); err != nil {
			return Result{}, err
		}
	}
	if result.Accounting != nil {
		if _, err := CanonicalAccounting(*result.Accounting); err != nil {
			return Result{}, err
		}
	}
	if err := result.RemoteAccounting.Validate(); err != nil {
		return Result{}, err
	}
	if result.ExecutionDurationMS != nil && *result.ExecutionDurationMS < 0 {
		return Result{}, fmt.Errorf("executor duration must be non-negative")
	}
	if result.RemoteTask != nil && result.RemoteTask.Pending && (result.RemoteTask.ID == "" || result.Error != "" || result.Session != nil) {
		return Result{}, fmt.Errorf("pending remote task requires an identity and cannot contain an error or local session")
	}
	return result, nil
}

// Progress uses the existing live log channel. Providers normalize their events
// into these records; consumers never parse provider protocols.
type Progress struct {
	OperationRef *RemoteTaskReference `json:"operationRef,omitempty"`
	Stream       string               `json:"stream,omitempty"`
	Text         string               `json:"text,omitempty"`
	Accounting   *Accounting          `json:"accounting,omitempty"`
}

// Request starts one invocation. Optional feature requirements are exact
// capability revisions. Session and remote references must never be emulated.
type Request struct {
	// Authorization is worker-owned and never copied from workflow params.
	Authorization *AuthorizationPolicy `json:"authorization,omitempty"`
	// TraceContext carries W3C traceparent/tracestate for optional adapter
	// instrumentation. It contains no baggage, credentials, or request content.
	TraceContext         map[string]string     `json:"traceContext,omitempty"`
	Environment          *EnvironmentManifest  `json:"environment,omitempty"`
	SessionInputs        []SessionInput        `json:"sessionInputs,omitempty"`
	Session              *SessionRequest       `json:"session,omitempty"`
	Action               string                `json:"action,omitempty"` // empty starts work; reconcile/poll query; steer delivers live input
	OperationAttemptID   string                `json:"operationAttemptId,omitempty"`
	Effect               string                `json:"effect,omitempty"`
	IdempotencyKey       string                `json:"idempotencyKey,omitempty"`
	ContractVersion      string                `json:"contractVersion,omitempty"`
	RequiredCapabilities map[string]string     `json:"requiredCapabilities,omitempty"`
	SessionID            string                `json:"sessionId,omitempty"`
	RemoteTask           *RemoteTaskReference  `json:"remoteTask,omitempty"`
	Secrets              map[string]string     `json:"secrets,omitempty"`
	HostExecutionAllowed bool                  `json:"hostExecutionAllowed,omitempty"`
	Workspace            string                `json:"workspace"`
	RunID                string                `json:"runId,omitempty"`
	ActivationID         string                `json:"activationId,omitempty"`
	AttemptID            string                `json:"attemptId,omitempty"`
	ExecutorVersion      string                `json:"executorVersion,omitempty"`
	Executor             string                `json:"executor"`
	Task                 string                `json:"task,omitempty"`
	Params               json.RawMessage       `json:"params,omitempty"`
	Inputs               map[string]any        `json:"inputs,omitempty"`
	Outputs              map[string]OutputDecl `json:"outputs,omitempty"`
	ScopePath            []string              `json:"scopePath,omitempty"`
	NodeID               string                `json:"nodeId,omitempty"`
	WorkDir              string                `json:"workDir"`
}

// Check enforces optional semantics before an invocation can start. Schema
// describes consumer validation; native typed generation must be requested
// explicitly through the typedResult capability.
func (r *Request) Check(m *AdapterManifest) error {
	if m != nil {
		if err := m.ValidateEnvironment(); err != nil {
			return err
		}
	}
	var available *EnvironmentManifest
	if m != nil {
		available = m.Environment
	}
	if err := CheckEnvironment(r.Environment, available); err != nil {
		return err
	}
	if err := r.CheckSession(); err != nil {
		return err
	}
	if err := r.CheckSessionInputs(); err != nil {
		return err
	}
	required := make(map[string]string, len(r.RequiredCapabilities))
	for name, version := range r.RequiredCapabilities {
		required[name] = version
	}
	require := func(name string) {
		if _, explicit := required[name]; !explicit {
			required[name] = Version
		}
	}
	if r.Action != "" && r.Action != "reconcile" && r.Action != "steer" && r.Action != "poll" {
		return fmt.Errorf("unsupported executor action %q", r.Action)
	}
	if len(r.SessionInputs) > 0 {
		if version, explicit := required[CapabilitySessionInput]; explicit && version != SessionInputVersion {
			return fmt.Errorf("session input requires capability version %s", SessionInputVersion)
		}
		required[CapabilitySessionInput] = SessionInputVersion
	}
	if r.Action == "steer" {
		if version, explicit := required[CapabilityLiveInput]; explicit && version != SessionInputVersion {
			return fmt.Errorf("live input requires capability version %s", SessionInputVersion)
		}
		required[CapabilityLiveInput] = SessionInputVersion
	}
	if r.Action == "reconcile" {
		require(CapabilityReconcile)
	}
	if r.Action == "poll" {
		if r.RemoteTask == nil || r.RemoteTask.ID == "" {
			return fmt.Errorf("poll requires a remote task identity")
		}
		require(CapabilityRemoteTask)
	}
	if _, async := required[CapabilityRemoteTask]; async {
		if r.Session != nil || r.SessionID != "" {
			return fmt.Errorf("remote handoff cannot detach a local session")
		}
		// Report the requested feature before its implied prerequisites. Map
		// iteration must not randomly hide remoteTask behind reconcile.
		if err := m.Check(r.Executor, r.ExecutorVersion, r.ContractVersion, map[string]string{CapabilityRemoteTask: required[CapabilityRemoteTask]}); err != nil {
			return err
		}
		require(CapabilityReconcile)
	}
	if (r.Effect == "idempotency-key" || r.Effect == "idempotent-with-key") && r.IdempotencyKey == "" {
		return fmt.Errorf("idempotent effect requires an idempotency key")
	}
	if r.IdempotencyKey != "" {
		require(CapabilityIdempotencyKey)
	}
	if r.SessionID != "" {
		require(CapabilitySession)
	}
	if r.Session != nil {
		if version, explicit := required[CapabilitySession]; explicit && version != SessionVersion {
			return fmt.Errorf("persistent sessions require session capability version %s", SessionVersion)
		}
		required[CapabilitySession] = SessionVersion
	}
	if r.RemoteTask != nil && r.Action != "reconcile" {
		require(CapabilityRemoteTask)
	}
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if len(r.Params) > 0 {
		if err := json.Unmarshal(r.Params, &params); err != nil {
			return fmt.Errorf("invalid execute params: %w", err)
		}
		if params.SessionID != "" {
			if _, async := required[CapabilityRemoteTask]; async {
				return fmt.Errorf("remote handoff cannot detach a local session")
			}
			require(CapabilitySession)
		}
	}
	for port, decl := range r.Outputs {
		if decl.Path != "" {
			if !m.Supports(CapabilityArtifacts) {
				return fmt.Errorf("%w (required by output port %q)", &UnsupportedCapabilityError{Executor: r.Executor, Capability: CapabilityArtifacts}, port)
			}
			if _, explicit := required[CapabilityArtifacts]; !explicit {
				required[CapabilityArtifacts] = Version
			}
		}
	}
	return m.Check(r.Executor, r.ExecutorVersion, r.ContractVersion, required)
}
