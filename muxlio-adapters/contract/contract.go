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
)

const Version = "1"

type OutputDecl struct {
	Mime   string          `json:"mime,omitempty"`
	Path   string          `json:"path,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// TokenUsage contains only reported counts. Nil means unavailable; &TokenUsage{}
// is a reported zero. Credits and context-window sizes are not token counts.
type TokenUsage struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cached int64 `json:"cached"`
}

type RemoteTaskReference struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

type Result struct {
	Outputs             map[string]any       `json:"outputs"`
	Error               string               `json:"error"`
	Logs                string               `json:"logs,omitempty"`
	Usage               *TokenUsage          `json:"usage,omitempty"`
	SessionID           string               `json:"sessionId,omitempty"`
	RemoteTask          *RemoteTaskReference `json:"remoteTask,omitempty"`
	ExecutionDurationMS *int64               `json:"executionDurationMs,omitempty"`
}

// ParseResult accepts exactly one result object. A JSON null is not a completed
// invocation, and invalid reported measurements must not become valid usage.
// Empty objects remain valid for executors with no outputs or usage to report.
func ParseResult(raw []byte) (Result, error) {
	var result Result
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return result, fmt.Errorf("executor result must be a JSON object")
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, fmt.Errorf("executor result: %w", err)
	}
	if u := result.Usage; u != nil && (u.Input < 0 || u.Output < 0 || u.Cached < 0) {
		return Result{}, fmt.Errorf("executor usage must be non-negative")
	}
	if result.ExecutionDurationMS != nil && *result.ExecutionDurationMS < 0 {
		return Result{}, fmt.Errorf("executor duration must be non-negative")
	}
	return result, nil
}

// Progress uses the existing live log channel. Providers normalize their events
// into these records; consumers never parse provider protocols.
type Progress struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// Request starts one invocation. Optional feature requirements are exact
// capability revisions. Session and remote references must never be emulated.
type Request struct {
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
	required := make(map[string]string, len(r.RequiredCapabilities))
	for name, version := range r.RequiredCapabilities {
		required[name] = version
	}
	require := func(name string) {
		if _, explicit := required[name]; !explicit {
			required[name] = Version
		}
	}
	if r.SessionID != "" {
		require(CapabilitySession)
	}
	if r.RemoteTask != nil {
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
