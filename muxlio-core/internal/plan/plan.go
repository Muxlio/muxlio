// Package plan defines the Muxlio Plan IR: the immutable, content-addressed
// workflow definition that the Go coordinator interprets. The IR is the
// canonical contract between the TypeScript authoring frontends and the Go
// execution engine. No JavaScript reaches the coordinator.
//
// IR v1.0 supports: execute, map, branch, loop, join, wait, child, project.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
)

// Plan is an immutable compiled workflow definition.
type Plan struct {
	Limits      *RunLimits `json:"limits,omitempty"`
	sourceJSON  json.RawMessage
	sourceTyped []byte
	// IRVersion is the Plan IR version this document conforms to.
	IRVersion string `json:"irVersion"`

	// Name is the human-readable workflow name.
	Name string `json:"name"`

	// Root names the entry subgraph within Subgraphs. Defaults to "root".
	Root string `json:"root"`

	// InputSchema is a JSON Schema describing the run input contract.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`

	// OutputSchema is a JSON Schema describing the run output contract.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`

	// DeadlineSeconds is the maximum wall-clock duration (in seconds) allowed
	// for a run from start to finish. When the deadline expires the run is
	// cancelled with a clear "run deadline exceeded" diagnostic. Zero means no
	// deadline. The window starts at persisted run creation and includes pauses.
	DeadlineSeconds *float64 `json:"deadlineSeconds,omitempty"`

	// Subgraphs maps named subgraphs. The root subgraph (key Plan.Root)
	// is the entry point; its output is the run output.
	Subgraphs map[string]Subgraph `json:"subgraphs"`

	// RequiredExecutors lists executor capabilities the plan needs.
	RequiredExecutors []string `json:"requiredExecutors,omitempty"`

	// Digest is the content digest of the canonical IR JSON, set by
	// the compiler or ComputeDigest. Plans with the same digest are
	// identical.
	Digest string `json:"digest,omitempty"`
}

// Subgraph is a named collection of nodes with an output projection.
type Subgraph struct {
	// Nodes are the nodes in this subgraph, processed in dependency order.
	Nodes []Node `json:"nodes"`

	// Output maps output port names to bindings. The root subgraph's
	// output is the run output.
	Output map[string]Binding `json:"output,omitempty"`
}

// Node is a single step or control-flow construct within a subgraph.
type Node struct {
	// ID is the stable node identifier within its subgraph. Used in
	// value references and activation identity.
	ID string `json:"id"`

	// Kind selects the node semantics: execute, map, branch, loop,
	// join, wait, child, project.
	Kind string `json:"kind"`

	// --- execute ---

	// Executor names the executor capability, e.g. "agent/claude".
	Executor             string                        `json:"executor,omitempty"`
	ContractVersion      string                        `json:"contractVersion,omitempty"`
	RequiredCapabilities map[string]string             `json:"requiredCapabilities,omitempty"`
	SessionID            string                        `json:"sessionId,omitempty"`
	RemoteTask           *contract.RemoteTaskReference `json:"remoteTask,omitempty"`
	ExecutorVersion      string                        `json:"executorVersion,omitempty"`
	PlacementLabels      map[string]string             `json:"placementLabels,omitempty"`
	WorkspaceMode        string                        `json:"workspaceMode,omitempty"`
	ConcurrencyKeys      []string                      `json:"concurrencyKeys,omitempty"`
	FallbackExecutors    []string                      `json:"fallbackExecutors,omitempty"`

	// Task is the natural-language or structured task description.
	Task string `json:"task,omitempty"`

	// Params is arbitrary JSON data passed to the executor.
	Params json.RawMessage `json:"params,omitempty"`

	// Inputs maps input names to bindings resolved before execution.
	Inputs map[string]Binding `json:"inputs,omitempty"`

	// Outputs declares named output ports and their file contracts.
	Outputs map[string]OutputDecl `json:"outputs,omitempty"`

	// --- map ---

	// Items is an expression evaluating to the collection to fan out over.
	Items *Expr `json:"items,omitempty"`

	// Key is an expression evaluating to a unique, stable key per item.
	Key *Expr `json:"key,omitempty"`

	// Concurrency limits parallel item processing.
	Concurrency *int `json:"concurrency,omitempty"`

	// MaxItems caps the collection size.
	MaxItems *int `json:"maxItems,omitempty"`

	// BodyRef names the subgraph invoked once per collection item.
	BodyRef string `json:"bodyRef,omitempty"`

	// --- branch ---

	// Condition is a pure predicate expression.
	Condition *Expr `json:"condition,omitempty"`

	// ThenRef names the subgraph executed when Condition is true.
	ThenRef string `json:"thenRef,omitempty"`

	// ElseRef names the subgraph executed when Condition is false.
	ElseRef string `json:"elseRef,omitempty"`

	// --- loop ---

	// While is a predicate evaluated before each iteration.
	While *Expr `json:"while,omitempty"`

	// InitState is the initial loop-carried state value.
	InitState *Expr `json:"initState,omitempty"`

	// MaxIterations caps the number of loop iterations.
	MaxIterations *int `json:"maxIterations,omitempty"`

	// StateRef names the port on the body subgraph output that becomes
	// the next iteration's state. If empty, the body output is used
	// directly.
	StateRef string `json:"stateRef,omitempty"`

	// OnExhausted selects behaviour when MaxIterations is reached while
	// While is still true: "pause" (resumable) or "fail".
	OnExhausted string `json:"onExhausted,omitempty"`

	// --- child ---

	// ChildRef names the subgraph invoked by a child node.
	ChildRef string `json:"childRef,omitempty"`

	// MaxDepth bounds recursive child invocations of the same subgraph.
	MaxDepth *int `json:"maxDepth,omitempty"`

	// --- retry / effect policy ---

	Retry *RetryPolicy `json:"retry,omitempty"`

	// TimeoutSeconds is the maximum wall-clock duration (in seconds) allowed
	// for a single execution attempt on this node. When the timeout expires
	// the attempt is cancelled with a diagnostic. External effect completion
	// remains unknown, so it is not automatically replayed. Zero means no timeout.
	// This is independent of the ScriptExecutor's global adapter timeout.
	TimeoutSeconds *float64 `json:"timeoutSeconds,omitempty"`
	// QueueTimeoutSeconds limits waiting for dispatch independently of execution.
	QueueTimeoutSeconds *float64 `json:"queueTimeoutSeconds,omitempty"`

	// --- wait ---

	// WaitFor selects the wait condition type: "timer", "signal", or "input".
	WaitFor string `json:"waitFor,omitempty"`

	// TimerSeconds is the duration in seconds for a timer wait.
	TimerSeconds *float64 `json:"timerSeconds,omitempty"`

	// SignalName is the name of the signal to wait for. For waitFor="input" it
	// names the input request, which carries a stable request ID, optional
	// response schema, expiration and required permissions.
	SignalName string `json:"signalName,omitempty"`

	// --- input request (waitFor="input") ---

	// RequestSchema is an optional JSON Schema validating responses submitted to
	// an input request. Malformed responses that fail validation are rejected
	// before they are accepted.
	RequestSchema json.RawMessage `json:"requestSchema,omitempty"`

	// RequestExpiresSeconds is the optional lifetime of an input request in
	// seconds. When it elapses without a response the request expires and the
	// wait fails. Zero means no expiration.
	RequestExpiresSeconds *float64 `json:"requestExpiresSeconds,omitempty"`

	// RequestPermissions lists the permissions an authenticated responder must
	// hold to submit a response. A caller missing any listed permission is
	// rejected. Empty means any authorized caller may respond.
	RequestPermissions []string `json:"requestPermissions,omitempty"`
}

// OutputDecl declares a named output port and its file contract.
type OutputDecl = contract.OutputDecl

// RetryPolicy defines retry behaviour for an execute node.
type RetryPolicy struct {
	MaxAttempts       int     `json:"maxAttempts,omitempty"`
	Effect            string  `json:"effect,omitempty"` // retryable, idempotency-key, reconcile, manual, none
	BackoffSeconds    float64 `json:"backoffSeconds,omitempty"`
	MaxBackoffSeconds float64 `json:"maxBackoffSeconds,omitempty"`

	// IdempotencyKey is a stable, plan-author-supplied key for the "idempotency-key"
	// effect policy. When set, the coordinator persists it alongside every attempt
	// record and sends it to adapters that support provider-side deduplication.
	// The key must be stable across retries; dynamic values must be embedded in
	// the node inputs rather than here.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// Binding is either a reference to another node's output port or a
// computed expression. Exactly one of Ref or Expr should be non-nil.
type Binding struct {
	Ref  *ValueRef `json:"ref,omitempty"`
	Expr *Expr     `json:"expr,omitempty"`
}

// ValueRef references a specific node's output port within the current
// scope. The scope is determined by the referencing node's position in
// the activation tree.
type ValueRef struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

// Resolve evaluates the binding against the given evaluation context.
func (b *Binding) Resolve(ctx *EvalContext) (any, error) {
	if b == nil {
		return nil, nil
	}
	if b.Ref != nil {
		nodeOut, ok := ctx.Outputs[b.Ref.Node]
		if !ok {
			return nil, fmt.Errorf("muxlio: no outputs for node %q", b.Ref.Node)
		}
		val, ok := nodeOut[b.Ref.Port]
		if !ok {
			return nil, fmt.Errorf("muxlio: no output port %q on node %q", b.Ref.Port, b.Ref.Node)
		}
		return val, nil
	}
	if b.Expr != nil {
		return b.Expr.Eval(ctx)
	}
	return nil, nil
}

// NodeKinds enumerates the supported node kinds.
const (
	// CurrentIRVersion is the only Plan IR version interpreted by this build.
	CurrentIRVersion = "1.0"

	KindExecute = "execute"
	KindMap     = "map"
	KindBranch  = "branch"
	KindLoop    = "loop"
	KindJoin    = "join"
	KindWait    = "wait"
	KindChild   = "child"
	KindProject = "project"
)

// RunStates enumerates run lifecycle states.
const (
	RunCreated   = "created"
	RunRunning   = "running"
	RunWaiting   = "waiting"
	RunPaused    = "paused"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// ActivationStates enumerates activation lifecycle states.
const (
	ActBlocked   = "blocked"
	ActQueued    = "queued"
	ActRunning   = "running"
	ActWaiting   = "waiting"
	ActSucceeded = "succeeded"
	ActFailed    = "failed"
	ActCancelled = "cancelled"
)

// ComputeDigest sets Plan.Digest to the SHA-256 of the canonical JSON
// encoding of the plan (with Digest zeroed). Returns the digest.
func (p *Plan) ComputeDigest() (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("muxlio: marshal plan: %w", err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	delete(value, "digest")
	data, err = canonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	p.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return p.Digest, nil
}

// VerifyDigest checks a supplied content digest without changing the plan.
// Plans without a digest remain accepted for locally authored/test plans.
func (p *Plan) VerifyDigest() error {
	if p == nil || p.Digest == "" {
		return nil
	}
	want := p.Digest
	copy := *p
	copy.Digest = ""
	got, err := copy.ComputeDigest()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("muxlio: plan digest mismatch: declared %q, computed %q", want, got)
	}
	return nil
}
