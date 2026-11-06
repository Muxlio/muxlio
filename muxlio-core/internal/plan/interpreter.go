package plan

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"strings"
	"sync"
	"time"

	"github.com/muxlio/muxlio-core/internal/coordinatorlog"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func generateRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("run_%x", b)
}

// errRunPaused stops subgraph execution after a loop marks the run paused.
var errRunPaused = errors.New("muxlio: run paused")

func activationID(runID string, scope []string, nodeID string) string {
	if len(scope) == 0 {
		return runID + ":" + nodeID
	}
	return runID + ":" + strings.Join(scope, ":") + ":" + nodeID
}

// inputRequestID returns a stable, URL-safe request ID for an input wait. The
// identity is derived from the activation and request name so a run retry
// (which preserves the activation) reuses the same request and any
// already-accepted response.
func inputRequestID(actID, name string) string {
	h := sha256.Sum256([]byte(actID + "\x00" + name))
	return "req_" + hex.EncodeToString(h[:8])
}

// Engine interprets a Plan IR against a Store. The default store is
// in-memory; pass a SQLite store for durable runs.
type Engine struct {
	budgetMu        sync.Mutex
	Context         context.Context
	plan            *Plan
	store           state.Store
	executor        workers.Executor
	ctxOnce         sync.Once
	ctx             context.Context
	cancel          context.CancelFunc
	controlMu       sync.Mutex
	cancelRequested bool
	paused          bool
	resumeCh        chan struct{}
}

// NewEngine creates an interpreter with an in-memory store.
func NewEngine(p *Plan, exec workers.Executor) *Engine {
	return NewEngineWithStore(p, exec, state.New())
}

// NewEngineWithStore creates an interpreter that persists through store.
func NewEngineWithStore(p *Plan, exec workers.Executor, store state.Store) *Engine {
	return &Engine{
		plan:     p,
		store:    store,
		executor: exec,
	}
}

// Store returns the engine's state store.
func (e *Engine) Store() state.Store { return e.store }

// Plan returns the plan being executed.
func (e *Engine) Plan() *Plan { return e.plan }

// CreateRun validates input, creates a run record, and returns its ID.
func (e *Engine) CreateRun(input json.RawMessage) (string, error) {
	if errs := Validate(e.plan); HasErrors(errs) {
		errOnly := ErrorsOnly(errs)
		return "", fmt.Errorf("muxlio: invalid plan: %s", errOnly[0].Message)
	}
	if err := e.plan.VerifyDigest(); err != nil {
		return "", err
	}
	if err := ValidateInput(e.plan, input); err != nil {
		return "", err
	}
	// Pin immutable bytes and digest before any run or executor side effects.
	if _, err := e.plan.ComputeDigest(); err != nil {
		return "", err
	}
	pinned, err := json.Marshal(e.plan)
	if err != nil {
		return "", err
	}
	var snapshot Plan
	if err := json.Unmarshal(pinned, &snapshot); err != nil {
		return "", err
	}
	e.plan = &snapshot
	var inputVal any
	if err := json.Unmarshal(input, &inputVal); err != nil {
		return "", fmt.Errorf("muxlio: invalid input JSON: %w", err)
	}
	runID := generateRunID()
	run := &state.Run{
		PlanJSON:     pinned,
		PlanDigest:   snapshot.Digest,
		ID:           runID,
		WorkflowName: e.plan.Name,
		Input:        inputVal,
	}
	if err := e.store.CreateRun(run); err != nil {
		return "", err
	}
	return runID, nil
}

// RepinRunPlan accepts only the existing pin. A different plan requires a new run.
func (e *Engine) RepinRunPlan(runID string, plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("muxlio: repin plan: nil plan")
	}
	if err := plan.VerifyDigest(); err != nil {
		return err
	}
	if _, err := plan.ComputeDigest(); err != nil {
		return err
	}
	if err := plan.VerifyDigest(); err != nil {
		return err
	}
	pinned, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	run, err := e.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run.State != RunPaused && run.State != RunCreated && run.State != RunRunning {
		return fmt.Errorf("muxlio: cannot repin plan for run in state %q", run.State)
	}
	if err := e.store.UpdateRunPlan(runID, pinned, plan.Digest); err != nil {
		return err
	}
	e.plan = plan
	return nil
}

// Retry resets failed or cancelled activations back to queued so Run can
// resume from the last succeeded checkpoint. Already-succeeded activations
// are left intact — their stored outputs are reused, so successfully
// completed work is never repeated.
//
// Retry also resets the run state from failed/cancelled to running so that
// Run() no longer treats the run as terminal.
func (e *Engine) Retry(runID string) error {
	run, err := e.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run.State != RunFailed && run.State != RunCancelled {
		return fmt.Errorf("muxlio: can only retry a failed or cancelled run, run %q is %q", runID, run.State)
	}
	// Restore the pinned plan so subsequent Run() uses the exact same bytes.
	if len(run.PlanJSON) == 0 {
		return fmt.Errorf("muxlio: retry: run %q has no pinned plan", runID)
	}
	var pinned Plan
	if err := json.Unmarshal(run.PlanJSON, &pinned); err != nil {
		return fmt.Errorf("muxlio: retry: pinned plan is corrupt: %w", err)
	}
	if err := pinned.VerifyDigest(); err != nil {
		return fmt.Errorf("muxlio: retry: pinned plan integrity check failed: %w", err)
	}
	if pinned.Digest == "" || pinned.Digest != run.PlanDigest {
		return fmt.Errorf("muxlio: retry: pinned plan digest mismatch")
	}
	e.plan = &pinned
	// Reset incomplete activations and transition the run atomically. This
	// also clears stale activation errors while preserving successful outputs.
	if err := e.store.RetryRun(runID); err != nil {
		return fmt.Errorf("muxlio: retry: resetting run: %w", err)
	}
	// Reset the internal context so Cancel() from the previous attempt does
	// not immediately abort this new attempt.
	e.ctxOnce = sync.Once{}
	e.ctx = nil
	e.cancel = nil
	e.cancelRequested = false
	return nil
}

// Run executes the run to completion. For M0 this is synchronous.
func (e *Engine) Run(runID string) error {
	run, err := e.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run.State == RunSucceeded || run.State == RunFailed || run.State == RunCancelled {
		return fmt.Errorf("muxlio: run %q is terminal", runID)
	}
	if len(run.PlanJSON) == 0 {
		return fmt.Errorf("muxlio: run %q has no pinned plan", runID)
	}
	if len(run.PlanJSON) > 0 {
		var pinned Plan
		if err := json.Unmarshal(run.PlanJSON, &pinned); err != nil {
			return err
		}
		if err := pinned.VerifyDigest(); err != nil {
			return err
		}
		if pinned.Digest == "" || pinned.Digest != run.PlanDigest {
			return fmt.Errorf("muxlio: pinned plan digest mismatch")
		}
		e.plan = &pinned
	}
	if e.plan.DeadlineSeconds != nil && *e.plan.DeadlineSeconds > 0 {
		e.applyRunDeadline(run.CreatedAt.Add(time.Duration(*e.plan.DeadlineSeconds * float64(time.Second))))
	}
	defer func() {
		if err := e.context().Err(); err != nil && e.scopeInterrupted() {
			_, _ = e.store.TransitionRunState(runID, []string{RunCreated, RunRunning, RunWaiting, RunPaused}, RunCancelled)
			_ = e.store.CancelRunWork(runID)
			reason := "run cancellation requested"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "run deadline exceeded"
			}
			_ = e.store.AppendEvent(runID, "run.interrupted", runID, map[string]any{"reason": reason, "externalEffects": "completion unknown; cancellation does not undo external effects"})
		}
	}()
	if err := e.checkpoint(); err != nil {
		return err
	}
	if run.State != RunPaused && run.State != RunRunning {
		changed, err := e.store.TransitionRunState(runID, []string{RunCreated, RunWaiting}, RunRunning)
		if err != nil {
			return err
		}
		if !changed {
			run, err = e.store.GetRun(runID)
			if err != nil {
				return err
			}
			if run.State == RunCancelled {
				return context.Canceled
			}
			if run.State != RunPaused && run.State != RunRunning {
				return fmt.Errorf("muxlio: run %q cannot start from state %q", runID, run.State)
			}
		}
	}

	rootName := e.plan.Root
	if rootName == "" {
		rootName = "root"
	}
	rootSG, ok := e.plan.Subgraphs[rootName]
	if !ok {
		err := fmt.Errorf("muxlio: root subgraph %q not found", rootName)
		e.store.SetRunError(runID, err)
		return err
	}

	run, err = e.store.GetRun(runID)
	if err != nil {
		return err
	}

	ctx := &EvalContext{
		Input:   run.Input,
		Outputs: make(map[string]map[string]any),
	}

	output, err := e.executeSubgraph(runID, rootName, rootSG, nil, ctx)
	if err != nil {
		if errors.Is(err, errRunPaused) {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		e.store.SetRunError(runID, err)
		return err
	}
	if err := e.checkpoint(); err != nil {
		return err
	}

	run, err = e.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run.State == RunPaused {
		return nil
	}

	// Recheck even if every activation was restored from a successful checkpoint.
	if e.plan.Limits != nil {
		attempts, err := e.store.ListExecutionAttempts(runID, "")
		if err != nil {
			return err
		}
		if err := e.checkUsage(attempts, false); err != nil {
			_ = e.store.SetRunError(runID, err)
			return err
		}
	}
	// Convention: if the root subgraph output has a single port, the run
	// output is that value. If multiple, the run output is the full map.
	var runOutput any
	if len(output) == 1 {
		for _, v := range output {
			runOutput = v
		}
	} else {
		runOutput = output
	}

	if err := e.store.SetRunOutput(runID, runOutput); err != nil {
		return err
	}
	if err := e.checkpoint(); err != nil {
		return err
	}
	changed, err := e.store.TransitionRunState(runID, []string{RunRunning, RunWaiting}, RunSucceeded)
	if err != nil {
		return err
	}
	if !changed {
		run, err := e.store.GetRun(runID)
		if err != nil {
			return err
		}
		if run.State == RunCancelled {
			return context.Canceled
		}
		return fmt.Errorf("muxlio: run %q cannot succeed from state %q", runID, run.State)
	}
	return nil
}

// executeSubgraph processes all nodes in dependency order, then evaluates
// the subgraph's output bindings.
func (e *Engine) executeSubgraph(runID, sgName string, sg Subgraph, scope []string, ctx *EvalContext) (map[string]any, error) {
	local := *ctx
	local.subgraph = sgName
	ctx = &local
	order, err := topoSort(sg.Nodes)
	if err != nil {
		return nil, fmt.Errorf("muxlio: subgraph %q: %w", sgName, err)
	}

	for i := range order {
		if err := e.checkpoint(); err != nil {
			return nil, err
		}
		if run, getErr := e.store.GetRun(runID); getErr == nil && run.State == RunPaused {
			return nil, errRunPaused
		}
		if run, getErr := e.store.GetRun(runID); getErr == nil && run.State == RunCancelled {
			return nil, context.Canceled
		}
		node := &order[i]
		actID := activationID(runID, scope, node.ID)
		if existing, err := e.store.GetActivation(actID); err == nil {
			if existing.State == ActSucceeded {
				ctx.Outputs[node.ID] = existing.Outputs
				continue
			}
			if existing.State == ActFailed || existing.State == ActCancelled {
				return nil, fmt.Errorf("muxlio: activation %q is terminal", actID)
			}
			// ActWaiting (e.g. exhausted loop): re-execute below.
		} else {
			resolved, err := EvalBindings(node.Inputs, ctx)
			if err != nil {
				return nil, err
			}
			inputs, err := json.Marshal(resolved)
			if err != nil {
				return nil, err
			}
			act := &state.Activation{
				Inputs:    inputs,
				ID:        actID,
				RunID:     runID,
				ScopePath: append([]string{}, scope...),
				NodeID:    node.ID,
				State:     ActQueued,
			}
			if err := e.admitActivation(act); err != nil {
				return nil, err
			}
		}
		if err := e.store.SetActivationState(actID, ActRunning); err != nil {
			return nil, err
		}

		nodeOutputs, err := e.executeNode(runID, node, scope, ctx)
		if err != nil {
			if errors.Is(err, errRunPaused) {
				return nil, errRunPaused
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if run, getErr := e.store.GetRun(runID); getErr == nil && run.State == RunCancelled {
					_ = e.store.SetActivationState(actID, ActCancelled)
				}
				return nil, err
			}
			_ = e.store.SetActivationError(actID, err)
			return nil, err
		}
		if err := e.checkpoint(); err != nil {
			return nil, err
		}
		if err := e.store.SetActivationOutputs(actID, nodeOutputs); err != nil {
			return nil, err
		}
		ctx.Outputs[node.ID] = nodeOutputs
		if err := e.checkpoint(); err != nil {
			return nil, err
		}
	}

	// Evaluate subgraph output bindings.
	output := make(map[string]any, len(sg.Output))
	for port, binding := range sg.Output {
		val, err := binding.Resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("muxlio: subgraph %q output port %q: %w", sgName, port, err)
		}
		output[port] = val
	}

	// If no explicit output, default to the last node's outputs.
	if len(sg.Output) == 0 && len(order) > 0 {
		last := order[len(order)-1]
		output = ctx.Outputs[last.ID]
	}

	return output, nil
}

// executeNode dispatches to the appropriate handler based on node kind.
func (e *Engine) executeNode(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	switch node.Kind {
	case KindExecute:
		return e.executeExecute(runID, node, scope, ctx)
	case KindMap:
		return e.executeMap(runID, node, scope, ctx)
	case KindBranch:
		return e.executeBranch(runID, node, scope, ctx)
	case KindLoop:
		return e.executeLoop(runID, node, scope, ctx)
	case KindProject:
		return e.executeProject(runID, node, scope, ctx)
	case KindJoin:
		return e.executeJoin(runID, node, scope, ctx)
	case KindWait:
		return e.executeWait(runID, node, scope, ctx)
	case KindChild:
		return e.executeChild(runID, node, scope, ctx)
	default:
		return nil, fmt.Errorf("muxlio: unknown node kind %q", node.Kind)
	}
}

// executeChild invokes a subgraph in a nested activation scope. Child
// recursion is bounded by MaxDepth (validated on every child node).
func (e *Engine) executeChild(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	sg, ok := e.plan.Subgraphs[node.ChildRef]
	if !ok {
		return nil, fmt.Errorf("muxlio: child subgraph %q not found", node.ChildRef)
	}
	depth := 0
	for _, part := range scope {
		if strings.HasPrefix(part, "child:") && strings.TrimPrefix(part, "child:") == node.ChildRef {
			depth++
		}
	}
	// The finite bound is a safety invariant. Enforce it at the runtime
	// entry point too, not only in Validate, so a plan that reaches
	// execution without a bound cannot recurse without limit.
	if node.MaxDepth == nil {
		return nil, fmt.Errorf("muxlio: child node %q has no maxDepth bound", node.ID)
	}
	if depth >= *node.MaxDepth {
		return nil, fmt.Errorf("muxlio: child node %q exceeds maxDepth %d", node.ID, *node.MaxDepth)
	}
	childScope := append(append([]string{}, scope...), "child:"+node.ChildRef)
	output, err := e.executeSubgraph(runID, node.ChildRef, sg, childScope, &EvalContext{
		Input: ctx.Input, Item: ctx.Item, State: ctx.State,
		Outputs: make(map[string]map[string]any),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": unwrapSubgraphOutput(output)}, nil
}

func unwrapSubgraphOutput(output map[string]any) any {
	if len(output) == 1 {
		for _, value := range output {
			return value
		}
	}
	return output
}

// executeExecute resolves inputs, calls the executor, and returns outputs.
// It honours the node's RetryPolicy for retryable failures.
func (e *Engine) executeExecute(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	var inputs map[string]any
	if act, getErr := e.store.GetActivation(activationID(runID, scope, node.ID)); getErr == nil && len(act.Inputs) > 0 {
		if err := json.Unmarshal(act.Inputs, &inputs); err != nil {
			return nil, err
		}
	} else {
		var err error
		inputs, err = EvalBindings(node.Inputs, ctx)
		if err != nil {
			return nil, err
		}
	}
	for key := range inputs {
		if strings.HasPrefix(key, "__after_") {
			delete(inputs, key)
		}
	}

	outDecls := make(map[string]workers.OutputDecl, len(node.Outputs))
	for port, decl := range node.Outputs {
		outDecls[port] = workers.OutputDecl{Mime: decl.Mime, Path: decl.Path, Schema: decl.Schema}
	}

	req := workers.ExecuteRequest{
		Context:              e.context(),
		LifetimeContext:      e.Context,
		Executor:             node.Executor,
		ExecutorVersion:      node.ExecutorVersion,
		ContractVersion:      node.ContractVersion,
		RequiredCapabilities: node.RequiredCapabilities,
		SessionID:            node.SessionID,
		RemoteTask:           node.RemoteTask,
		PlacementLabels:      node.PlacementLabels,
		WorkspaceMode:        node.WorkspaceMode,
		ConcurrencyKeys:      node.ConcurrencyKeys,
		Task:                 node.Task,
		Params:               node.Params,
		Inputs:               inputs,
		Outputs:              outDecls,
		ScopePath:            scope,
		NodeID:               node.ID,
	}
	if node.Retry != nil {
		req.Effect = node.Retry.Effect
	}
	if ws := workspaceFromInputs(inputs); ws != "" {
		req.Workspace = ws
		if req.PlacementLabels == nil {
			req.PlacementLabels = make(map[string]string)
		}
		req.PlacementLabels[workers.WorkspacePlacementLabel] = ws
	}
	var executionConfig struct {
		Policy     *workers.ExecutionPolicy `json:"executionPolicy"`
		SecretRefs map[string]string        `json:"secretRefs"`
	}
	if len(node.Params) > 0 {
		if err := json.Unmarshal(node.Params, &executionConfig); err != nil {
			return nil, fmt.Errorf("muxlio: invalid execute params: %w", err)
		}
	}
	req.Policy = executionConfig.Policy
	req.SecretRefs = executionConfig.SecretRefs

	maxAttempts := 1
	// Legacy retryable failures retain their retry budget. Reconciliation only
	// permits another dispatch after affirmative evidence of no external effect.
	if node.Retry != nil && node.Retry.MaxAttempts > 1 && (node.Retry.Effect == "" || node.Retry.Effect == "retryable" || node.Retry.Effect == "pure" || node.Retry.Effect == "idempotency-key" || node.Retry.Effect == "idempotent-with-key" || node.Retry.Effect == "reconcile" || node.Retry.Effect == "reconcilable") {
		maxAttempts = node.Retry.MaxAttempts
	}

	// Keys identify a logical activation across every worker and run retry.
	idempotencyKey := ""
	if req.Effect == "idempotency-key" || req.Effect == "idempotent-with-key" {
		idempotencyKey = node.Retry.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = activationID(runID, scope, node.ID)
		}
	}
	req.IdempotencyKey = idempotencyKey
	actID := activationID(runID, scope, node.ID)
	recoveredBase := 0
	prior, priorErr := e.store.ListExecutionAttempts(runID, actID)
	if priorErr != nil {
		return nil, priorErr
	}
	for i := len(prior) - 1; i >= 0; i-- {
		a := prior[i]
		// The durable operation identity takes precedence on recovery, including
		// after an explicit run retry or while reattaching a queued dispatch.
		if a.IdempotencyKey != "" {
			idempotencyKey = a.IdempotencyKey
			req.IdempotencyKey = idempotencyKey
		}
		if a.State == "succeeded" && a.Result != nil && a.Result.ExecutionSucceeded {
			result := workers.ExecuteResult{Outputs: a.Result.Outputs}
			if err := ValidateExecuteResult(node.Outputs, result); err != nil {
				return nil, err
			}
			return result.Outputs, nil
		}
		if a.State == "resolved" {
			// Resolution may have committed immediately before coordinator loss.
			// Never reuse its fenced dispatch identity for the next operation.
			recoveredBase = a.RetryNumber
			break
		}
		if a.AwaitReconciliationReason != "" || a.State == "running" || a.State == "awaiting_reconciliation" {
			if _, queued := e.executor.(workers.QueuedExecutor); queued && a.State == "running" && a.AwaitReconciliationReason == "" {
				recoveredBase = a.RetryNumber - 1
				break
			}
			// A direct replay is a fresh invocation even when its effect is safe.
			// Preserve the logical idempotency key but charge a new attempt slot.
			if a.Effect == "none" || a.Effect == "pure" || ((a.Effect == "idempotency-key" || a.Effect == "idempotent-with-key") && a.IdempotencyKey != "") {
				if a.IdempotencyKey != "" {
					idempotencyKey = a.IdempotencyKey
					req.IdempotencyKey = idempotencyKey
				}
				recoveredBase = a.RetryNumber
				break
			}
			req.RunID, req.ActivationID, req.AttemptID, req.Executor = runID, actID, a.ID, a.Executor
			result, safe, err := e.reconcileEffect(req, a, node)
			if err != nil {
				return nil, err
			}
			if !safe {
				return result.Outputs, nil
			}
			recoveredBase = a.RetryNumber
			break
		}
	}

	// Each attempt receives a fresh value; executor mutation cannot alter retries.
	inputSnapshot, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	var lastErr error
	attemptBase := recoveredBase
	if act, err := e.store.GetActivation(actID); err == nil {
		if act.AttemptBase > attemptBase {
			attemptBase = act.AttemptBase
		}
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req.Inputs = nil
		if err := json.Unmarshal(inputSnapshot, &req.Inputs); err != nil {
			return nil, err
		}
		if attempt > 1 {
			if err := e.wait(retryDelay(node.Retry, attempt-1, lastErr)); err != nil {
				return nil, err
			}
		}
		executor := node.Executor
		if attempt > 1 && len(node.FallbackExecutors) > 0 && (req.Effect == "" || req.Effect == "retryable" || req.Effect == "pure" || req.Effect == "none") {
			fallbackIndex := attempt - 2
			if fallbackIndex >= len(node.FallbackExecutors) {
				fallbackIndex = len(node.FallbackExecutors) - 1
			}
			executor = node.FallbackExecutors[fallbackIndex]
		}
		if attempt > 1 {
			coordinatorlog.AttemptRetry(runID, actID, executor, attempt)
		}
		req.Executor = executor
		req.RunID = runID
		req.ActivationID = actID
		req.AttemptID = fmt.Sprintf("%s-attempt-%d", actID, attemptBase+attempt)
		guarded, guardErr := workers.GuardRequest(req)
		if guardErr != nil {
			return nil, fmt.Errorf("muxlio: execution policy: %w", guardErr)
		}
		coordinatorlog.RegisterSecrets(guarded.Secrets)
		started := time.Now()
		if recordErr := e.admitAttempt(state.ExecutionAttempt{ID: req.AttemptID, RunID: runID, ActivationID: actID, RetryNumber: attemptBase + attempt, Executor: executor, State: "running", StartedAt: started, Effect: req.Effect, IdempotencyKey: idempotencyKey}); recordErr != nil {
			return nil, recordErr
		}
		logSink := workers.NewStoreLogSink(e.store, runID, actID, guarded.Secrets, req.AttemptID)
		guarded.LogSink = logSink
		guarded.TimeoutSeconds = node.TimeoutSeconds
		guarded.QueueTimeoutSeconds = node.QueueTimeoutSeconds
		attemptID := req.AttemptID
		guarded.OnExecutionReturned = func() {
			_ = e.store.AppendEvent(runID, "execution.returned", attemptID, map[string]any{"externalEffects": "completion not established by executor return"})
		}
		result, err := workers.ExecuteAttempt(e.executor, guarded)
		safeRedispatch := false
		var uncertain workers.UncertainExecutionError
		if errors.As(err, &uncertain) {
			if req.Effect == "pure" || req.Effect == "none" || (req.IdempotencyKey != "" && (req.Effect == "idempotency-key" || req.Effect == "idempotent-with-key")) {
				safeRedispatch = true
			} else {
				a := state.ExecutionAttempt{ID: req.AttemptID, RunID: runID, ActivationID: actID, RetryNumber: attemptBase + attempt, Executor: executor, StartedAt: started, Effect: req.Effect, IdempotencyKey: idempotencyKey, OperationRef: result.RemoteTask, Usage: result.Usage, Measurements: result.Measurements}
				var reconciled workers.ExecuteResult
				reconciled, safeRedispatch, err = e.reconcileEffect(guarded, a, node)
				if err == nil && !safeRedispatch {
					// Lookup usage belongs to its own invocation, not the original operation.
					observed, measurements := result.Usage, result.Measurements
					result = reconciled
					result.Usage, result.Measurements = observed, measurements
				}
				if safeRedispatch {
					err = fmt.Errorf("previous operation confirmed not applied")
				}
			}
		}

		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			_ = e.store.AppendEvent(runID, "execution.cancellation_requested", req.AttemptID, map[string]any{"reason": err.Error(), "termination": "unconfirmed", "externalEffects": "completion unknown"})
		}
		_ = logSink.Close()
		// Hosting coordinator shutdown suspends the durable dispatch. Its result
		// may already be checkpointed, so let the queued executor recover it
		// before deciding whether the external operation needs reconciliation.
		if _, queued := e.executor.(workers.QueuedExecutor); queued && e.Context != nil && e.Context.Err() != nil {
			return nil, e.Context.Err()
		}
		// Check engine-level cancellation first: run cancel or run deadline.
		// These must terminate the retry loop immediately.
		if ctxErr := e.context().Err(); ctxErr != nil {
			failureMsg := ctxErr.Error()
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				failureMsg = fmt.Sprintf("run deadline exceeded (node %q, attempt %d)", node.ID, attemptBase+attempt)
			}
			_ = e.store.RecordExecutionAttempt(state.ExecutionAttempt{ID: req.AttemptID, RunID: runID, ActivationID: actID, RetryNumber: attemptBase + attempt, Executor: executor, State: "cancelled", StartedAt: started, FinishedAt: time.Now(), Failure: failureMsg, Usage: result.Usage, Measurements: result.Measurements, ExecutionDurationMS: result.ExecutionDurationMS, Effect: req.Effect, IdempotencyKey: idempotencyKey, OperationRef: result.RemoteTask, AwaitReconciliationReason: awaitReconciliationReason(req.Effect, node.ID, req.AttemptID)})
			return nil, ctxErr
		}
		// Per-node execution timeout: record the attempt as cancelled with an
		// actionable diagnostic. Do not replay while external completion is unknown.
		if errors.Is(err, context.DeadlineExceeded) {
			failureMsg := fmt.Sprintf("execution interrupted (node %q, attempt %d): %v; external effect completion unknown", node.ID, attemptBase+attempt, err)
			_ = e.store.RecordExecutionAttempt(state.ExecutionAttempt{ID: req.AttemptID, RunID: runID, ActivationID: actID, RetryNumber: attemptBase + attempt, Executor: executor, State: "cancelled", StartedAt: started, FinishedAt: time.Now(), Failure: failureMsg, Usage: result.Usage, Measurements: result.Measurements, ExecutionDurationMS: result.ExecutionDurationMS, Effect: req.Effect, IdempotencyKey: idempotencyKey, OperationRef: result.RemoteTask, AwaitReconciliationReason: awaitReconciliationReason(req.Effect, node.ID, req.AttemptID)})
			lastErr = fmt.Errorf("muxlio: %s", failureMsg)
			return nil, lastErr
		}
		if errors.Is(err, context.Canceled) {
			_ = e.store.RecordExecutionAttempt(state.ExecutionAttempt{ID: req.AttemptID, RunID: runID, ActivationID: actID, RetryNumber: attemptBase + attempt, Executor: executor, State: "cancelled", StartedAt: started, FinishedAt: time.Now(), Failure: err.Error(), Usage: result.Usage, Measurements: result.Measurements, ExecutionDurationMS: result.ExecutionDurationMS, Effect: req.Effect, IdempotencyKey: idempotencyKey, OperationRef: result.RemoteTask, AwaitReconciliationReason: awaitReconciliationReason(req.Effect, node.ID, req.AttemptID)})
			return nil, err
		}
		if result.Logs != "" {
			if logErr := workers.AppendBoundedExecutorLog(e.store, runID, actID, attempt, result.Logs, guarded.Secrets, req.AttemptID); logErr != nil {
				return nil, logErr
			}
		}
		failure := ""
		if err != nil {
			failure = diagnostic.Redact(workers.RedactSecrets(err.Error(), guarded.Secrets))
		} else if result.Error != "" {
			failure = diagnostic.Redact(workers.RedactSecrets(result.Error, guarded.Secrets))
		}
		attemptResult := &state.AttemptResult{ExecutionSucceeded: failure == "", Outputs: result.Outputs}
		var validationErr error
		if failure == "" {
			validationErr = ValidateExecuteResult(node.Outputs, result)
			if validationErr != nil {
				failure = diagnostic.Redact(workers.RedactSecrets(validationErr.Error(), guarded.Secrets))
				var diagnostic *ValueValidationFailure
				if errors.As(validationErr, &diagnostic) {
					attemptResult.ValidationFailure = diagnostic
				}
			}
		}
		attemptState := "succeeded"
		if safeRedispatch && (req.Effect == "reconcile" || req.Effect == "reconcilable") {
			// Keep the durable not-applied checkpoint if shutdown interrupts the
			// gap before the next dispatch. Recovery must advance the attempt ID.
			attemptState = "resolved"
		}
		if failure != "" {
			if attemptState != "resolved" {
				attemptState = "failed"
			}
			d := diagnostic.New("MUXLIO_EXECUTION_FAILED", "runtime", failure, "Repair the executor failure and retry the run.")
			var typed *diagnostic.Diagnostic
			if errors.As(err, &typed) {
				d = *typed
			}
			d.RunID = runID
			d.ScopeID = actID
			d.AttemptID = req.AttemptID
			d.NodeID = node.ID
			d.Subgraph = ctx.subgraph
			if d.Requirements == nil {
				d.Requirements = node.RequiredCapabilities
			}
			if v := attemptResult.ValidationFailure; v != nil {
				d.Code = v.Code
				d.Expected = v.Expected
				d.Actual = v.Actual
				d.ValuePath = v.Path
				if v.Missing {
					d.Actual = "<missing>"
				}
				d.Fix = "Return the declared output ports with values matching their JSON Schemas."
			}
			cause := err
			for depth := 0; cause != nil && depth < 8; depth++ {
				d.Causes = append(d.Causes, diagnostic.New("MUXLIO_CAUSE", "runtime", cause.Error(), ""))
				cause = errors.Unwrap(cause)
			}
			if sg, ok := e.plan.Subgraphs[ctx.subgraph]; ok {
				for i, sourceNode := range sg.Nodes {
					if sourceNode.ID == node.ID {
						pointer := fmt.Sprintf("/subgraphs/%s/nodes/%d", pointerPart(ctx.subgraph), i)
						d.Source = sourcePosition(e.plan.sourceJSON, pointer, sourceOffsets(e.plan.sourceJSON))
						break
					}
				}
			}
			d.Identify()
			var safeDiagnostic diagnostic.Diagnostic
			if decodeErr := json.Unmarshal(diagnostic.WithSecrets(&d, guarded.Secrets), &safeDiagnostic); decodeErr != nil {
				return nil, fmt.Errorf("decode sanitized diagnostic: %w", decodeErr)
			}
			attemptResult.Diagnostic = &safeDiagnostic
			sanitized := diagnostic.WithSecrets(attemptResult, guarded.Secrets)
			var safeResult state.AttemptResult
			if decodeErr := json.Unmarshal(sanitized, &safeResult); decodeErr != nil {
				return nil, fmt.Errorf("decode sanitized attempt diagnostic: %w", decodeErr)
			}
			// Keep the returned typed validation error safe even if the combined
			// payload budget omitted validationFailure after the diagnostic.
			if attemptResult.ValidationFailure != nil {
				var safeValidation ValueValidationFailure
				if decodeErr := json.Unmarshal(diagnostic.WithSecrets(attemptResult.ValidationFailure, guarded.Secrets), &safeValidation); decodeErr != nil {
					return nil, fmt.Errorf("decode sanitized validation failure: %w", decodeErr)
				}
				validationErr = &safeValidation
			}
			attemptResult = &safeResult

		}
		var unresolved workers.UncertainExecutionError
		if errors.As(err, &unresolved) && !safeRedispatch {
			return nil, err
		}
		heldReason := ""
		if failure != "" && !safeRedispatch && (req.Effect == "manual" || req.Effect == "nonretryable" || req.Effect == "reconcile" || req.Effect == "reconcilable") {
			heldReason = awaitReconciliationReason(req.Effect, node.ID, req.AttemptID)
			attemptState = "awaiting_reconciliation"
		}
		if recordErr := e.store.RecordExecutionAttempt(state.ExecutionAttempt{ID: fmt.Sprintf("%s-attempt-%d", actID, attemptBase+attempt), RunID: runID, ActivationID: actID, RetryNumber: attemptBase + attempt, Executor: executor, State: attemptState, StartedAt: started, FinishedAt: time.Now(), Failure: failure, Result: attemptResult, Usage: result.Usage, Measurements: result.Measurements, ExecutionDurationMS: result.ExecutionDurationMS, Effect: req.Effect, IdempotencyKey: idempotencyKey, OperationRef: result.RemoteTask, AwaitReconciliationReason: heldReason}); recordErr != nil {
			return nil, recordErr
		}
		if e.plan.Limits != nil {
			attempts, err := e.store.ListExecutionAttempts(runID, "")
			if err != nil {
				return nil, err
			}
			if err := e.checkUsage(attempts, false); err != nil {
				return nil, err
			}
		}

		if err != nil {
			lastErr = fmt.Errorf("muxlio: executor %q attempt %d failed: %s", executor, attempt, diagnostic.Redact(workers.RedactSecrets(err.Error(), guarded.Secrets)))
			if attempt == maxAttempts || (!safeRedispatch && (req.Effect == "reconcile" || req.Effect == "reconcilable")) {
				return nil, lastErr
			}
			continue
		}
		if result.Error != "" {
			lastErr = fmt.Errorf("muxlio: executor %q attempt %d returned error: %s", executor, attempt, diagnostic.Redact(workers.RedactSecrets(result.Error, guarded.Secrets)))
			if attempt == maxAttempts || (!safeRedispatch && (req.Effect == "reconcile" || req.Effect == "reconcilable")) {
				return nil, lastErr
			}
			continue
		}
		if validationErr != nil {
			lastErr = fmt.Errorf("muxlio: executor %q output validation: %w", executor, validationErr)
			if attempt == maxAttempts || (!safeRedispatch && (req.Effect == "reconcile" || req.Effect == "reconcilable")) {
				return nil, lastErr
			}
			continue
		}
		return result.Outputs, nil
	}
	return nil, lastErr
}

// Explicit retry policy supplies bounded exponential pacing. Throttled
// providers also receive a short default delay before a configured fallback.
func retryDelay(policy *RetryPolicy, failedAttempts int, failure error) time.Duration {
	if policy == nil || failedAttempts < 1 {
		return 0
	}
	base := policy.BackoffSeconds
	if base <= 0 && failure != nil {
		message := strings.ToLower(failure.Error())
		if strings.Contains(message, "429") || strings.Contains(message, "rate limit") || strings.Contains(message, "throttl") || strings.Contains(message, "overloaded") {
			base = 1
		}
	}
	if base <= 0 {
		return 0
	}
	limit := policy.MaxBackoffSeconds
	if limit <= 0 || limit > 60 {
		limit = 60
	}
	for i := 1; i < failedAttempts && base < limit; i++ {
		base *= 2
	}
	if base > limit {
		base = limit
	}
	return time.Duration(base * float64(time.Second))
}

// awaitReconciliationReason returns a non-empty human-readable explanation
// when the given effect policy requires explicit operator resolution after an
// uncertain execution outcome. An empty return means no hold is needed (the
// coordinator will retry or surface the failure directly).
//
// The reason is stored on the ExecutionAttempt record so operators querying
// ListExecutionAttempts can understand why the attempt is pending without
// inspecting coordinator internals.
func awaitReconciliationReason(effect, nodeID, attemptID string) string {
	switch effect {
	case "pure", "none", "idempotency-key", "idempotent-with-key":
		return ""
	default:
		return fmt.Sprintf("node %q attempt %q (effect %q): external completion is unknown; adapter reconciliation or explicit operator resolution is required before retry", nodeID, attemptID, effect)
	}
}

func inheritOutputs(parent *EvalContext) map[string]map[string]any {
	if parent == nil || len(parent.Outputs) == 0 {
		return make(map[string]map[string]any)
	}
	out := make(map[string]map[string]any, len(parent.Outputs))
	for k, v := range parent.Outputs {
		cp := make(map[string]any, len(v))
		for pk, pv := range v {
			cp[pk] = pv
		}
		out[k] = cp
	}
	return out
}

// executeMap evaluates the collection, fans out the body subgraph per item,
// and collects results into an array.
func (e *Engine) executeMap(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	itemsVal, err := node.Items.Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: evaluating map items: %w", err)
	}
	items, ok := itemsVal.([]any)
	if !ok {
		return nil, fmt.Errorf("muxlio: map items must be an array, got %T", itemsVal)
	}

	if node.MaxItems != nil && len(items) > *node.MaxItems {
		return nil, fmt.Errorf("muxlio: map collection has %d items, exceeds maxItems %d", len(items), *node.MaxItems)
	}

	bodySG, ok := e.plan.Subgraphs[node.BodyRef]
	if !ok {
		return nil, fmt.Errorf("muxlio: map body subgraph %q not found", node.BodyRef)
	}

	seenKeys := make(map[string]bool)
	keys := make([]string, len(items))
	for i, item := range items {
		// Compute key.
		var keyStr string
		if node.Key != nil {
			keyVal, err := node.Key.Eval(&EvalContext{
				Input:   ctx.Input,
				Item:    item,
				Outputs: ctx.Outputs,
			})
			if err != nil {
				return nil, fmt.Errorf("muxlio: evaluating map key: %w", err)
			}
			keyStr = fmt.Sprintf("%v", keyVal)
		} else {
			keyStr = fmt.Sprintf("%d", i)
		}

		if seenKeys[keyStr] {
			return nil, fmt.Errorf("muxlio: map collection produced duplicate key %q", keyStr)
		}
		seenKeys[keyStr] = true
		keys[i] = keyStr
	}

	concurrency := 1
	if node.Concurrency != nil {
		concurrency = *node.Concurrency
	}
	results := make([]any, len(items))
	if concurrency <= 1 {
		for i, item := range items {
			itemScope := append(append([]string{}, scope...), fmt.Sprintf("map:%s:%s", node.ID, keys[i]))
			itemCtx := &EvalContext{
				Input:   ctx.Input,
				Item:    item,
				Outputs: inheritOutputs(ctx),
			}
			itemOutput, err := e.executeSubgraph(runID, node.BodyRef, bodySG, itemScope, itemCtx)
			if err != nil {
				return nil, err
			}
			results[i] = unwrapSubgraphOutput(itemOutput)
		}
	} else {
		type mapResult struct {
			index  int
			output any
			err    error
		}
		resultCh := make(chan mapResult, len(items))
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup

		for i, item := range items {
			sem <- struct{}{}
			wg.Add(1)
			go func(index int, item any) {
				defer wg.Done()
				defer func() { <-sem }()
				itemScope := append(append([]string{}, scope...), fmt.Sprintf("map:%s:%s", node.ID, keys[index]))
				itemCtx := &EvalContext{
					Input:   ctx.Input,
					Item:    item,
					Outputs: inheritOutputs(ctx),
				}
				itemOutput, err := e.executeSubgraph(runID, node.BodyRef, bodySG, itemScope, itemCtx)
				resultCh <- mapResult{index: index, output: unwrapSubgraphOutput(itemOutput), err: err}
			}(i, item)
		}
		wg.Wait()
		close(resultCh)
		var firstErr error
		for result := range resultCh {
			if result.err != nil && firstErr == nil {
				firstErr = result.err
			}
			results[result.index] = result.output
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}

	if err := e.store.AppendEvent(runID, state.EventMapExpanded, activationID(runID, scope, node.ID), map[string]any{
		"nodeId":      node.ID,
		"count":       len(results),
		"concurrency": concurrency,
	}); err != nil {
		return nil, err
	}

	return map[string]any{"items": results}, nil
}

// executeBranch evaluates the condition and runs the selected subgraph.
func (e *Engine) executeBranch(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	condVal, err := node.Condition.Eval(ctx)
	if err != nil {
		return nil, fmt.Errorf("muxlio: evaluating branch condition: %w", err)
	}
	cond, ok := condVal.(bool)
	if !ok {
		return nil, fmt.Errorf("muxlio: branch condition must be boolean, got %T", condVal)
	}

	var sgName string
	if cond {
		sgName = node.ThenRef
	} else {
		sgName = node.ElseRef
	}

	if sgName == "" {
		// No else branch and condition is false — empty output.
		return map[string]any{}, nil
	}

	sg, ok := e.plan.Subgraphs[sgName]
	if !ok {
		return nil, fmt.Errorf("muxlio: branch subgraph %q not found", sgName)
	}

	branchScope := append(append([]string{}, scope...), fmt.Sprintf("branch:%s:%t", node.ID, cond))

	output, err := e.executeSubgraph(runID, sgName, sg, branchScope, &EvalContext{
		Input:   ctx.Input,
		Item:    ctx.Item,
		State:   ctx.State,
		Outputs: inheritOutputs(ctx),
	})
	if err != nil {
		return nil, err
	}

	if err := e.store.AppendEvent(runID, state.EventBranchSelected, activationID(runID, scope, node.ID), map[string]any{
		"nodeId": node.ID,
		"branch": cond,
	}); err != nil {
		return nil, err
	}

	// Unwrap single-port output for convenience.
	var result any
	if len(output) == 1 {
		for _, v := range output {
			result = v
		}
	} else {
		result = output
	}
	return map[string]any{"result": result}, nil
}

// loopProgressKey is stored on loop activation outputs when a loop pauses exhausted.
const loopProgressKey = "__muxlioLoopProgress"

// executeLoop runs the body subgraph repeatedly with loop-carried state.
func (e *Engine) executeLoop(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	maxIter := 0
	if node.MaxIterations != nil {
		maxIter = *node.MaxIterations
	}

	actID := activationID(runID, scope, node.ID)

	iteration := 0
	var currentState any
	if existing, err := e.store.GetActivation(actID); err == nil && existing.Outputs != nil {
		if prog, state, ok := loopProgressFromOutputs(existing.Outputs); ok {
			switch v := prog["iteration"].(type) {
			case float64:
				iteration = int(v)
			case int:
				iteration = v
			}
			currentState = state
		}
	}
	if currentState == nil && node.InitState != nil {
		val, err := node.InitState.Eval(ctx)
		if err != nil {
			return nil, fmt.Errorf("muxlio: evaluating loop initState: %w", err)
		}
		currentState = val
	}

	bodySG, ok := e.plan.Subgraphs[node.BodyRef]
	if !ok {
		return nil, fmt.Errorf("muxlio: loop body subgraph %q not found", node.BodyRef)
	}

	exhausted := false
	for {
		if iteration >= maxIter {
			if maxIter > 0 {
				whileCtx := &EvalContext{
					Input:   ctx.Input,
					State:   currentState,
					Outputs: ctx.Outputs,
				}
				condVal, err := node.While.Eval(whileCtx)
				if err != nil {
					return nil, fmt.Errorf("muxlio: evaluating loop while: %w", err)
				}
				if cond, ok := condVal.(bool); ok && cond {
					exhausted = true
				}
			}
			break
		}

		// Evaluate while condition with current state.
		whileCtx := &EvalContext{
			Input:   ctx.Input,
			State:   currentState,
			Outputs: ctx.Outputs,
		}
		condVal, err := node.While.Eval(whileCtx)
		if err != nil {
			return nil, fmt.Errorf("muxlio: evaluating loop while: %w", err)
		}
		cond, ok := condVal.(bool)
		if !ok {
			return nil, fmt.Errorf("muxlio: loop while must be boolean, got %T", condVal)
		}
		if !cond {
			break
		}

		// Execute body subgraph.
		iterScope := append(append([]string{}, scope...), fmt.Sprintf("loop:%s:%d", node.ID, iteration))
		iterCtx := &EvalContext{
			Input:   ctx.Input,
			State:   currentState,
			Outputs: inheritOutputs(ctx),
		}

		bodyOutput, err := e.executeSubgraph(runID, node.BodyRef, bodySG, iterScope, iterCtx)
		if err != nil {
			return nil, err
		}

		// Extract next state.
		if node.StateRef != "" {
			next, ok := bodyOutput[node.StateRef]
			if !ok {
				return nil, fmt.Errorf("muxlio: loop body missing state port %q", node.StateRef)
			}
			currentState = next
		} else if len(bodyOutput) == 1 {
			for _, v := range bodyOutput {
				currentState = v
			}
		} else {
			currentState = bodyOutput
		}

		iteration++
	}

	if err := e.store.AppendEvent(runID, state.EventLoopIterated, activationID(runID, scope, node.ID), map[string]any{
		"nodeId":    node.ID,
		"iteration": iteration,
		"exhausted": exhausted,
	}); err != nil {
		return nil, err
	}

	if exhausted {
		switch node.OnExhausted {
		case "fail":
			return nil, fmt.Errorf("muxlio: loop %q exhausted after %d iterations", node.ID, iteration)
		case "pause", "":
			checkpoint := map[string]any{
				"result": currentState,
				loopProgressKey: map[string]any{
					"iteration": iteration,
					"exhausted": true,
				},
			}
			if err := e.store.SetActivationOutputs(actID, checkpoint); err != nil {
				return nil, err
			}
			if err := e.store.SetActivationState(actID, ActWaiting); err != nil {
				return nil, err
			}
			if _, err := e.store.TransitionRunState(runID, []string{RunRunning, RunWaiting}, RunPaused); err != nil {
				return nil, err
			}
			return nil, errRunPaused
		}
	}

	return map[string]any{"result": currentState}, nil
}

func loopProgressFromOutputs(outputs map[string]any) (progress map[string]any, state any, ok bool) {
	if outputs == nil {
		return nil, nil, false
	}
	prog, _ := outputs[loopProgressKey].(map[string]any)
	if prog == nil {
		return nil, nil, false
	}
	exhausted, _ := prog["exhausted"].(bool)
	if !exhausted {
		return nil, nil, false
	}
	state = outputs["result"]
	return prog, state, true
}

func workspaceFromInputs(inputs map[string]any) string {
	v, ok := inputs["workspace"]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if d, ok := val["data"].(string); ok {
			return d
		}
	}
	return ""
}

// executeProject resolves input bindings and passes them through as outputs.
func (e *Engine) executeProject(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	return EvalBindings(node.Inputs, ctx)
}

// executeJoin collects resolved inputs from all dependencies.
func (e *Engine) executeJoin(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	return EvalBindings(node.Inputs, ctx)
}

// executeWait handles timer and signal wait nodes. For a timer it schedules
// a durable deadline then blocks until it fires. For a signal it blocks until
// a signal with the matching name is delivered to the run.
//
// In the synchronous M1 interpreter, "blocking" means polling the store
// directly (timers: sleep until due; signals: check store after delivery).
// A proper async scheduler replaces this with an event-driven loop in M2+.
func (e *Engine) executeWait(runID string, node *Node, scope []string, ctx *EvalContext) (map[string]any, error) {
	actID := activationID(runID, scope, node.ID)

	switch node.WaitFor {
	case "timer":
		if node.TimerSeconds == nil {
			return nil, fmt.Errorf("muxlio: wait node %q: timerSeconds required for timer wait", node.ID)
		}
		dur := time.Duration(*node.TimerSeconds * float64(time.Second))
		due := time.Now().Add(dur)
		timerID := actID + ":timer"
		if err := e.store.ScheduleTimer(timerID, runID, actID, due, nil); err != nil {
			return nil, fmt.Errorf("muxlio: scheduling timer: %w", err)
		}
		// A process may stop here: recovery uses the original durable deadline.
		timer, err := e.store.GetTimer(timerID)
		if err != nil {
			return nil, err
		}
		if timer.State == "cancelled" {
			return nil, fmt.Errorf("muxlio: timer %q cancelled", timerID)
		}
		if err := e.store.SetActivationState(actID, ActWaiting); err != nil {
			return nil, err
		}
		if _, err := e.store.TransitionRunState(runID, []string{RunRunning}, RunWaiting); err != nil {
			return nil, err
		}
		if remaining := time.Until(timer.DueAt); remaining > 0 && timer.State != "fired" {
			if err := e.wait(remaining); err != nil {
				return nil, err
			}
		}
		if _, err := e.store.TransitionRunState(runID, []string{RunWaiting}, RunRunning); err != nil {
			return nil, err
		}
		// Drain the timer from the store.
		if _, err := e.store.DueTimers(time.Now()); err != nil {
			return nil, fmt.Errorf("muxlio: firing timer: %w", err)
		}
		return map[string]any{"fired": true}, nil

	case "signal":
		if node.SignalName == "" {
			return nil, fmt.Errorf("muxlio: wait node %q: signalName required for signal wait", node.ID)
		}
		// Check if the signal is already waiting — avoid unnecessary state
		// transitions for the common "pre-delivered" case used in tests.
		sigs, err := e.store.ConsumeSignals(runID, node.SignalName)
		if err != nil {
			return nil, fmt.Errorf("muxlio: consuming signal %q: %w", node.SignalName, err)
		}
		if len(sigs) > 0 {
			return map[string]any{"payload": sigs[len(sigs)-1].Payload}, nil
		}
		// Signal not yet present: transition run and activation to waiting so
		// observers (UI, status API) can see that the run is blocked. Then poll
		// until the signal arrives or context is cancelled.
		if _, err := e.store.TransitionRunState(runID, []string{RunRunning, RunWaiting}, RunWaiting); err != nil {
			return nil, fmt.Errorf("muxlio: transitioning to waiting for signal %q: %w", node.SignalName, err)
		}
		if err := e.store.SetActivationState(actID, ActWaiting); err != nil {
			return nil, fmt.Errorf("muxlio: setting activation waiting for signal %q: %w", node.SignalName, err)
		}
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-e.context().Done():
				return nil, e.context().Err()
			case <-ticker.C:
				if err := e.checkpoint(); err != nil {
					return nil, err
				}
				sigs, err := e.store.ConsumeSignals(runID, node.SignalName)
				if err != nil {
					return nil, fmt.Errorf("muxlio: consuming signal %q: %w", node.SignalName, err)
				}
				if len(sigs) > 0 {
					// Restore run to running so the rest of the subgraph continues.
					if _, err := e.store.TransitionRunState(runID, []string{RunWaiting}, RunRunning); err != nil {
						return nil, fmt.Errorf("muxlio: resuming from signal wait: %w", err)
					}
					return map[string]any{"payload": sigs[len(sigs)-1].Payload}, nil
				}
			}
		}

	case "input":
		if node.SignalName == "" {
			return nil, fmt.Errorf("muxlio: wait node %q: signalName required for input wait", node.ID)
		}
		reqID := inputRequestID(actID, node.SignalName)
		// Create the durable input request. Creation is idempotent, so a run
		// retry reuses the same request and any already-accepted response.
		req := &state.InputRequest{
			RunID:        runID,
			ActivationID: actID,
			RequestID:    reqID,
			Name:         node.SignalName,
			Schema:       node.RequestSchema,
			Permissions:  node.RequestPermissions,
		}
		if node.RequestExpiresSeconds != nil && *node.RequestExpiresSeconds > 0 {
			due := time.Now().Add(time.Duration(*node.RequestExpiresSeconds * float64(time.Second)))
			req.ExpiresAt = &due
		}
		if err := e.store.CreateInputRequest(req); err != nil {
			return nil, fmt.Errorf("muxlio: creating input request %q: %w", reqID, err)
		}
		// Re-read to pick up any pre-submitted response or prior acceptance.
		current, err := e.store.GetInputRequest(runID, reqID)
		if err != nil {
			return nil, fmt.Errorf("muxlio: loading input request %q: %w", reqID, err)
		}
		if current.State == state.InputRequestFulfilled {
			return map[string]any{"response": current.Response}, nil
		}
		if current.State == state.InputRequestExpired || current.State == state.InputRequestCancelled {
			return nil, fmt.Errorf("muxlio: input request %q is %s", reqID, current.State)
		}
		// Wait for a response without dispatching a worker: the run and
		// activation enter the waiting state so observers see a blocked run that
		// consumes no worker slot.
		if _, err := e.store.TransitionRunState(runID, []string{RunRunning, RunWaiting}, RunWaiting); err != nil {
			return nil, fmt.Errorf("muxlio: transitioning to waiting for input %q: %w", node.SignalName, err)
		}
		if err := e.store.SetActivationState(actID, ActWaiting); err != nil {
			return nil, fmt.Errorf("muxlio: setting activation waiting for input %q: %w", node.SignalName, err)
		}
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-e.context().Done():
				return nil, e.context().Err()
			case <-ticker.C:
				if err := e.checkpoint(); err != nil {
					return nil, err
				}
				// Expire pending requests whose deadline elapsed without a response.
				if _, err := e.store.ExpireInputRequests(time.Now()); err != nil {
					return nil, fmt.Errorf("muxlio: expiring input requests: %w", err)
				}
				current, err := e.store.GetInputRequest(runID, reqID)
				if err != nil {
					return nil, fmt.Errorf("muxlio: polling input request %q: %w", reqID, err)
				}
				if current.State == state.InputRequestFulfilled {
					if _, err := e.store.TransitionRunState(runID, []string{RunWaiting}, RunRunning); err != nil {
						return nil, fmt.Errorf("muxlio: resuming from input wait: %w", err)
					}
					return map[string]any{"response": current.Response}, nil
				}
				if current.State == state.InputRequestExpired || current.State == state.InputRequestCancelled {
					return nil, fmt.Errorf("muxlio: input request %q is %s", reqID, current.State)
				}
			}
		}

	default:
		// Passthrough: no wait condition configured; treat as a no-op projection.
		return EvalBindings(node.Inputs, ctx)
	}
}

// --- dependency resolution and topological sort ---

// nodeDeps returns the node IDs that a node references in its inputs,
// items, key, condition, while, and initState expressions.
func nodeDeps(node Node) []string {
	var deps []string
	add := func(ids ...string) {
		for _, id := range ids {
			if id != "" {
				deps = append(deps, id)
			}
		}
	}
	for _, b := range node.Inputs {
		if b.Ref != nil {
			add(b.Ref.Node)
		}
		if b.Expr != nil {
			add(exprDeps(b.Expr)...)
		}
	}
	if node.Items != nil {
		add(exprDeps(node.Items)...)
	}
	if node.Key != nil {
		add(exprDeps(node.Key)...)
	}
	if node.Condition != nil {
		add(exprDeps(node.Condition)...)
	}
	if node.While != nil {
		add(exprDeps(node.While)...)
	}
	if node.InitState != nil {
		add(exprDeps(node.InitState)...)
	}
	return deps
}

// exprDeps recursively collects node IDs referenced by "ref" ops.
func exprDeps(e *Expr) []string {
	if e == nil {
		return nil
	}
	var deps []string
	if e.Op == "ref" {
		deps = append(deps, e.Node)
	}
	deps = append(deps, exprDeps(e.Expr)...)
	deps = append(deps, exprDeps(e.Left)...)
	deps = append(deps, exprDeps(e.Right)...)
	for _, sub := range e.Fields {
		deps = append(deps, exprDeps(sub)...)
	}
	for _, sub := range e.Elements {
		deps = append(deps, exprDeps(sub)...)
	}
	return deps
}

// topoSort returns nodes in dependency order. Returns an error if there
// is a cycle.
func topoSort(nodes []Node) ([]Node, error) {
	byID := make(map[string]int, len(nodes))
	for i, n := range nodes {
		byID[n.ID] = i
	}

	// Build adjacency: dep -> dependents
	dependents := make(map[string][]string, len(nodes))
	inDegree := make(map[string]int, len(nodes))
	for _, n := range nodes {
		inDegree[n.ID] = 0
	}
	for _, n := range nodes {
		seen := make(map[string]bool)
		for _, dep := range nodeDeps(n) {
			if dep == n.ID {
				return nil, fmt.Errorf("muxlio: node %q depends on itself", n.ID)
			}
			if _, ok := byID[dep]; !ok {
				continue // external ref (e.g. parent scope); skip
			}
			if seen[dep] {
				continue
			}
			seen[dep] = true
			dependents[dep] = append(dependents[dep], n.ID)
			inDegree[n.ID]++
		}
	}

	// Kahn's algorithm.
	var queue []string
	for _, n := range nodes {
		if inDegree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}

	var order []Node
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, nodes[byID[id]])
		for _, dep := range dependents[id] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(order) != len(nodes) {
		// Find cycle participants for error message.
		var cyclic []string
		for _, n := range nodes {
			if inDegree[n.ID] > 0 {
				cyclic = append(cyclic, n.ID)
			}
		}
		return nil, fmt.Errorf("muxlio: dependency cycle among nodes: %s", strings.Join(cyclic, ", "))
	}

	return order, nil
}
