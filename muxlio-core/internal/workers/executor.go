// Package workers defines the executor interface and a fake executor for
// testing. In production, executor adapters dispatch to local subprocesses,
// remote workers, HTTP endpoints, or agent CLIs. The interpreter calls the
// Executor interface; it does not know the transport.
package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

// ExecuteRequest is the envelope sent to an executor.
type ExecuteRequest struct {
	SessionInputs        []contract.SessionInput       `json:"sessionInputs,omitempty"`
	Action               string                        `json:"action,omitempty"`
	OperationAttemptID   string                        `json:"operationAttemptId,omitempty"`
	IdempotencyKey       string                        `json:"idempotencyKey,omitempty"`
	ContractVersion      string                        `json:"contractVersion,omitempty"`
	RequiredCapabilities map[string]string             `json:"requiredCapabilities,omitempty"`
	Session              *contract.SessionRequest      `json:"session,omitempty"`
	SessionID            string                        `json:"sessionId,omitempty"`
	RemoteTask           *contract.RemoteTaskReference `json:"remoteTask,omitempty"`
	// LifetimeContext is the hosting coordinator lifetime. Its cancellation
	// suspends durable dispatch for recovery instead of revoking workflow intent.
	LifetimeContext     context.Context `json:"-"`
	ExecutionDeadline   *time.Time      `json:"executionDeadline,omitempty"`
	OnExecutionReturned func()          `json:"-"`
	// TimeoutSeconds starts when execution begins, excluding dispatch queue time.
	TimeoutSeconds      *float64 `json:"timeoutSeconds,omitempty"`
	QueueTimeoutSeconds *float64 `json:"queueTimeoutSeconds,omitempty"`
	// Deadline survives transport; Context cancellation still revokes assignments.
	Deadline      *time.Time `json:"deadline,omitempty"`
	QueueDeadline *time.Time `json:"queueDeadline,omitempty"`
	// Effect classifies replay safety; unknown policies fail closed after loss.
	Effect          string          `json:"effect,omitempty"`
	Context         context.Context `json:"-"`
	Workspace       string          `json:"workspace,omitempty"`
	WorkspaceMode   string          `json:"workspaceMode,omitempty"`
	ConcurrencyKeys []string        `json:"concurrencyKeys,omitempty"`
	// resourcesAdmitted is set only after receiving a coordinator assignment.
	// Direct script calls cannot enforce durable, cross-worker reservations.
	resourcesAdmitted bool
	// attemptScratch is owned by one invocation and shared with artifact transfer.
	attemptScratch  string
	redactions      map[string]string
	RunID           string            `json:"runId,omitempty"`
	ActivationID    string            `json:"activationId,omitempty"`
	AttemptID       string            `json:"attemptId,omitempty"`
	Executor        string            `json:"executor"`
	ExecutorVersion string            `json:"executorVersion,omitempty"`
	PlacementLabels map[string]string `json:"placementLabels,omitempty"`
	Task            string            `json:"task,omitempty"`
	Params          json.RawMessage   `json:"params,omitempty"`
	Inputs          map[string]any    `json:"inputs,omitempty"`
	// Secrets contains transient values for local execution only. Durable and
	// remote requests carry SecretRefs resolved by operator policy on the worker.
	Secrets    map[string]string     `json:"-"`
	SecretRefs map[string]string     `json:"secretRefs,omitempty"`
	Policy     *ExecutionPolicy      `json:"executionPolicy,omitempty"`
	Outputs    map[string]OutputDecl `json:"outputs,omitempty"`
	ScopePath  []string              `json:"scopePath,omitempty"`
	NodeID     string                `json:"nodeId,omitempty"`
	// LogSink receives incremental output while the attempt is running.
	LogSink LogSink `json:"-"`
}

// ExecutionPolicy is the worker-side allowlist for data and capabilities.
// Required policies fail closed when the executor cannot enforce them.
type ExecutionPolicy struct {
	Trust               string   `json:"trust,omitempty"`
	Network             string   `json:"network,omitempty"`
	Filesystem          string   `json:"filesystem,omitempty"`
	Required            bool     `json:"required,omitempty"`
	AllowedSecrets      []string `json:"allowedSecrets,omitempty"`
	AllowedCapabilities []string `json:"allowedCapabilities,omitempty"`
}

// GuardRequest enforces explicit capability/secret allowlists and returns a
// copy safe to pass to an executor. A required policy must name the selected
// executor and every secret being propagated.
func GuardRequest(req ExecuteRequest) (ExecuteRequest, error) {
	if err := (&contract.Request{Executor: req.Executor, Session: req.Session, SessionID: req.SessionID, Params: req.Params}).CheckSession(); err != nil {
		return ExecuteRequest{}, err
	}
	var configErr error
	req, configErr = executionSecurityConfig(req)
	if configErr != nil {
		return ExecuteRequest{}, configErr
	}
	if req.Policy == nil {
		if len(req.Secrets) != 0 || len(req.SecretRefs) != 0 {
			return ExecuteRequest{}, fmt.Errorf("execution policy required to propagate secrets")
		}
		return req, nil
	}
	policy := req.Policy
	if err := ValidatePermissions(req); err != nil {
		return ExecuteRequest{}, err
	}
	capabilityAllowed := false
	for _, capability := range policy.AllowedCapabilities {
		if capability == req.Executor {
			capabilityAllowed = true
		}
	}
	if policy.Required && !capabilityAllowed {
		return ExecuteRequest{}, fmt.Errorf("execution policy does not allow capability %q", req.Executor)
	}
	allowed := make(map[string]bool, len(policy.AllowedSecrets))
	for _, name := range policy.AllowedSecrets {
		allowed[name] = true
	}
	filtered := make(map[string]string)
	for name, value := range req.Secrets {
		if !allowed[name] {
			if policy.Required {
				return ExecuteRequest{}, fmt.Errorf("execution policy does not allow secret %q", name)
			}
			continue
		}
		filtered[name] = value
	}
	refs := make(map[string]string)
	for name, handle := range req.SecretRefs {
		if !allowed[name] || handle == "" {
			return ExecuteRequest{}, fmt.Errorf("execution policy does not allow secret handle")
		}
		refs[name] = handle
	}
	req.SecretRefs = refs
	req.Secrets = filtered
	return req, nil
}

// OutputDecl declares a named output port and its file contract.
type OutputDecl = contract.OutputDecl

// ExecuteResult is what an executor returns.
type ExecuteResult = contract.Result

// UncertainExecutionError requires reconciliation before another attempt can
// safely run: the previous worker may already have performed its effects.
type UncertainExecutionError struct{}

func (UncertainExecutionError) Error() string {
	return "execution outcome uncertain; manual reconciliation required"
}

// Executor is the interface for executing workflow steps.
type Executor interface {
	Execute(req ExecuteRequest) (ExecuteResult, error)
}

// FakeExecutor is a test executor that returns canned responses keyed by
// node ID or executor name. It supports per-node fixtures and can simulate
// failures for testing retry logic.
type FakeExecutor struct {
	mu      sync.Mutex
	byNode  map[string]ExecuteResult
	byExec  map[string]ExecuteResult
	calls   []ExecuteRequest
	failOn  map[string]error
	callCnt map[string]int
}

// NewFake creates a fake executor.
func NewFake() *FakeExecutor {
	return &FakeExecutor{
		byNode:  make(map[string]ExecuteResult),
		byExec:  make(map[string]ExecuteResult),
		failOn:  make(map[string]error),
		callCnt: make(map[string]int),
	}
}

// ByNode registers a canned result for a specific node ID.
func (f *FakeExecutor) ByNode(nodeID string, result ExecuteResult) *FakeExecutor {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byNode[nodeID] = result
	return f
}

// ByExecutor registers a canned result for a specific executor capability.
func (f *FakeExecutor) ByExecutor(executor string, result ExecuteResult) *FakeExecutor {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byExec[executor] = result
	return f
}

// FailOnNode makes the executor return an error on the Nth call to a node.
// If maxCalls is 0, it always fails.
func (f *FakeExecutor) FailOnNode(nodeID string, err error, maxCalls int) *FakeExecutor {
	f.mu.Lock()
	defer f.mu.Unlock()
	if maxCalls <= 0 {
		f.failOn[nodeID] = err
	} else {
		f.failOn[fmt.Sprintf("%s:%d", nodeID, maxCalls)] = err
	}
	return f
}

// Calls returns all execute requests received.
func (f *FakeExecutor) Calls() []ExecuteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ExecuteRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// Execute implements Executor.
func (f *FakeExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	started := time.Now()
	f.calls = append(f.calls, req)
	f.callCnt[req.NodeID]++

	// Check for forced failures.
	if err, ok := f.failOn[req.NodeID]; ok {
		return f.measured(started, ExecuteResult{}), err
	}
	key := fmt.Sprintf("%s:%d", req.NodeID, f.callCnt[req.NodeID])
	if err, ok := f.failOn[key]; ok {
		return f.measured(started, ExecuteResult{}), err
	}

	// Look up by node ID first, then by executor.
	if result, ok := f.byNode[req.NodeID]; ok {
		return f.measured(started, result), nil
	}
	if result, ok := f.byExec[req.Executor]; ok {
		return f.measured(started, result), nil
	}
	// Default: produce empty outputs for each declared port.
	outputs := make(map[string]any)
	for port, decl := range req.Outputs {
		outputs[port] = map[string]any{
			"digest": "sha256:fake",
			"mime":   decl.Mime,
			"path":   decl.Path,
		}
	}
	return f.measured(started, ExecuteResult{Outputs: outputs}), nil
}

func (f *FakeExecutor) measured(started time.Time, result ExecuteResult) ExecuteResult {
	ms := time.Since(started).Milliseconds()
	result.ExecutionDurationMS = &ms
	return result
}

// CompileExecutor is a simple executor that evaluates all inputs and passes
// them through as outputs. Useful for "project"-style steps that just need
// to produce JSON values without external effects.
type CompileExecutor struct{}

func (CompileExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	return ExecuteResult{Outputs: req.Inputs}, nil
}
