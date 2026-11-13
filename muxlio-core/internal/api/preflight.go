package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// PreflightRequest is the input to a preflight check. Plan is required; Input
// defaults to {} when omitted, matching submission.
type PreflightRequest struct {
	WorkspaceID string          `json:"workspaceId,omitempty"`
	Plan        plan.Plan       `json:"plan"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// PreflightResponse is the output of a preflight check. It is always returned
// as 200 OK; the client inspects Feasible and the per-executor Executors list
// to decide whether to proceed. No run is created and no work is dispatched.
//
// Feasible is true only when all of the following hold:
//   - The plan has no structural errors.
//   - Input conforms to the plan's input schema.
//   - Every executor referenced by plan nodes has at least one live, ready
//     worker that satisfies all matching constraints.
type PreflightResponse struct {
	// Feasible is true when the plan is valid, the effective input is valid,
	// and every execute node is satisfiable by at least one live worker.
	Feasible          bool            `json:"feasible"`
	WorkspaceID       string          `json:"workspaceId,omitempty"`
	WorkspaceWorkerID string          `json:"workspaceWorkerId,omitempty"`
	WorkspaceError    string          `json:"workspaceError,omitempty"`
	WorkerError       string          `json:"workerError,omitempty"`
	InputSchema       json.RawMessage `json:"inputSchema,omitempty"`
	Deferred          []string        `json:"deferred"`

	// PlanIssues contains structural plan-validation diagnostics. Fatal errors
	// prevent execution; warnings are informational.
	PlanIssues []planIssueSummary `json:"planIssues,omitempty"`

	// InputError is set when the effective input fails schema validation.
	InputError string `json:"inputError,omitempty"`

	// Executors reports the per-executor feasibility analysis. Each entry
	// corresponds to one execute node; requirements are never merged across nodes.
	Executors []ExecutorPreflight `json:"executors,omitempty"`

	// WorkersChecked is the count of live workers consulted. Zero means the
	// coordinator has no worker information (store does not support it) or
	// no workers have registered.
	WorkersChecked int `json:"workersChecked"`

	// Verified lists validations performed, including ones reporting failures.
	// Worker evidence is cached; feasibility never guarantees execution success.
	Verified []string `json:"verified"`

	// Unverified lists aspects that could only be confirmed at execution time
	// (e.g. current provider authentication state, fine-grained permission
	// grants, remote quota, or concurrent capacity pressure).
	Unverified []string `json:"unverified"`
}

// planIssueSummary is a compact summary of a plan.ValidationError suitable
// for the preflight response. The original diagnostic fields are preserved.
type planIssueSummary struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Phase    string `json:"phase,omitempty"`
	Subgraph string `json:"subgraph,omitempty"`
	NodeID   string `json:"nodeId,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// ExecutorPreflight reports one execute node's requirements and eligible workers.
type ExecutorPreflight struct {
	// Executor is the capability name (e.g. "agent/test").
	Executor       string                    `json:"executor"`
	Subgraph       string                    `json:"subgraph"`
	Policy         *workers.ExecutionPolicy  `json:"executionPolicy,omitempty"`
	WorkerEvidence []PreflightWorkerEvidence `json:"workerEvidence,omitempty"`
	Diagnostics    []string                  `json:"diagnostics,omitempty"`

	// NodeIDs lists the plan node IDs that use this executor.
	NodeIDs []string `json:"nodeIds"`

	// Satisfiable is true when at least one live, ready worker can accept
	// dispatches for this executor under all stated constraints.
	Satisfiable bool `json:"satisfiable"`

	// BlockingReasons is non-empty when Satisfiable is false. Each entry
	// names one class of constraint that no live worker satisfies.
	BlockingReasons []string `json:"blockingReasons,omitempty"`

	// MatchingWorkers lists compatible live workers. Capacity is not reserved.
	MatchingWorkers []string `json:"matchingWorkers,omitempty"`

	// Constraints summarises the requirements extracted from the plan nodes.
	Constraints ExecutorConstraints `json:"constraints"`
}

// PreflightWorkerEvidence preserves the worker-reported contract and last probe.
// It is a snapshot, not proof of current authentication or tool authorization.
type PreflightWorkerEvidence struct {
	WorkerID   string                 `json:"workerId"`
	LastSeen   time.Time              `json:"lastSeen"`
	Capability state.WorkerCapability `json:"capability"`
}

// ExecutorConstraints captures the matching requirements derived from plan
// nodes for a single executor.
type ExecutorConstraints struct {
	WorkspaceMode string `json:"workspaceMode,omitempty"`
	// RequiredCapabilities are the optional adapter capabilities all matching
	// nodes require (e.g. {"session":"2","artifacts":"1"}).
	RequiredCapabilities map[string]string `json:"requiredCapabilities,omitempty"`

	// ExecutorVersion is the exact version pin required, if any.
	ExecutorVersion string `json:"executorVersion,omitempty"`

	// PlacementLabels are the worker-label constraints, if any.
	PlacementLabels map[string]string `json:"placementLabels,omitempty"`

	// IsolationRequired is true when at least one node requires enforced
	// sandbox isolation.
	IsolationRequired bool `json:"isolationRequired,omitempty"`

	// IsolationRuntime is the sandbox runtime name when IsolationRequired is
	// true (e.g. "bwrap", "oci").
	IsolationRuntime string `json:"isolationRuntime,omitempty"`

	// WorkspaceRequired is true when at least one node requires a workspace
	// mount (WorkspaceMode is not "empty").
	WorkspaceRequired bool `json:"workspaceRequired,omitempty"`
}

// executorRequirements holds the dispatch constraints for one execute node.
type executorRequirements struct {
	nodeIDs              []string
	subgraph             string
	request              workers.ExecuteRequest
	requiredCapabilities map[string]string
	executorVersion      string
	placementLabels      map[string]string
	isolationRequired    bool
	isolationRuntime     string
	workspaceRequired    bool
}

// Keep nodes independent: different version pins, labels, policies, and
// isolation settings on the same executor may require different workers.
func collectExecutorRequirements(p *plan.Plan) []*executorRequirements {
	var out []*executorRequirements
	for sg, graph := range p.Subgraphs {
		for _, node := range graph.Nodes {
			if node.Kind != "execute" || node.Executor == "" {
				continue
			}
			req := &executorRequirements{
				nodeIDs: []string{node.ID}, subgraph: sg,
				requiredCapabilities: node.RequiredCapabilities, executorVersion: node.ExecutorVersion,
				placementLabels: node.PlacementLabels, workspaceRequired: node.WorkspaceMode != "empty",
				request: workers.ExecuteRequest{Executor: node.Executor, ExecutorVersion: node.ExecutorVersion,
					ContractVersion: node.ContractVersion, RequiredCapabilities: node.RequiredCapabilities,
					Environment: node.Environment, Session: node.Session, SessionID: node.SessionID, RemoteTask: node.RemoteTask,
					PlacementLabels: node.PlacementLabels, WorkspaceMode: node.WorkspaceMode,
					ConcurrencyKeys: node.ConcurrencyKeys, Params: node.Params},
			}
			req.request.Outputs = make(map[string]workers.OutputDecl, len(node.Outputs))
			for port, output := range node.Outputs {
				req.request.Outputs[port] = workers.OutputDecl{Mime: output.Mime, Path: output.Path, Schema: output.Schema}
			}
			if node.Retry != nil {
				req.request.Effect = node.Retry.Effect
				if node.Retry.Effect == "idempotency-key" || node.Retry.Effect == "idempotent-with-key" {
					// Only presence matters to contract validation; no key is reserved.
					req.request.IdempotencyKey = "preflight"
				}
			}
			var params struct {
				Isolation *struct {
					Mode    string
					Runtime string
				}
			}
			if json.Unmarshal(node.Params, &params) == nil && params.Isolation != nil {
				req.isolationRequired = params.Isolation.Mode == "required"
				req.isolationRuntime = params.Isolation.Runtime
			}
			out = append(out, req)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].subgraph != out[j].subgraph {
			return out[i].subgraph < out[j].subgraph
		}
		return out[i].nodeIDs[0] < out[j].nodeIDs[0]
	})
	return out
}

// PreflightRun validates that a plan and its input can be executed given the
// currently registered workers. It performs no destructive operations and
// creates no run. It is safe to call before submitting a run.
//
// Without a worker registry, execute nodes cannot be considered feasible.
// Use Coordinator.PreflightRun to include workspace authorization.
func PreflightRun(p *plan.Plan, input json.RawMessage, store state.Store) PreflightResponse {
	return preflightRun(p, input, store, nil)
}

func preflightRun(p *plan.Plan, input json.RawMessage, store any, coordinator *Coordinator) PreflightResponse {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	resp := PreflightResponse{
		Verified:    []string{"plan_structural_validity"},
		InputSchema: p.InputSchema,
		Deferred:    []string{"dynamic_input_bindings", "worker_secret_resolution", "current_tool_authorization", "execution_environment_and_dependencies"},
		Unverified:  unverifiedItems,
	}

	// Work on a copy; workspace authorization must not mutate the caller plan.
	copy := *p
	p = &copy
	if err := p.VerifyDigest(); err != nil {
		resp.WorkspaceError = err.Error()
	}
	if coordinator != nil && resp.WorkspaceError == "" {
		if err := coordinator.AuthorizePlanWorkspace(p, input); err != nil {
			resp.WorkspaceError = err.Error()
		} else {
			resp.WorkspaceID = p.WorkspaceID
			resp.Verified = append(resp.Verified, "workspace_authorization")
			if p.WorkspaceID != "" {
				_, binding, err := coordinator.workspaceRegistry().Resolve(p.WorkspaceID)
				if err == nil {
					resp.WorkspaceWorkerID = binding.WorkerID
				}
			}
		}
	} else if coordinator == nil {
		resp.Unverified = append(append([]string{}, resp.Unverified...), "workspace_authorization")
	}
	// 1. Structural plan validation.
	issues := plan.Validate(p)
	// Submission must be able to pin the plan even when no digest was supplied.
	if _, err := p.ComputeDigest(); err != nil {
		issues = append(issues, plan.ValidationError{
			Severity: "error", Code: "MUXLIO_PLAN_DIGEST", Phase: "plan",
			Message: "computing plan digest: " + err.Error(),
			Fix:     "Use plan values supported by the coordinator's canonical JSON encoding.",
		})
	}
	for _, issue := range issues {
		resp.PlanIssues = append(resp.PlanIssues, planIssueSummary{
			Severity: issue.Severity,
			Code:     issue.Code,
			Phase:    issue.Phase,
			Subgraph: issue.Subgraph,
			NodeID:   issue.NodeID,
			Message:  issue.Message,
			Fix:      issue.Fix,
		})
	}
	planHasErrors := plan.HasErrors(issues)

	// 2. Validate the input that would be submitted.
	if len(input) > 0 {
		resp.Verified = append(resp.Verified, "input_schema_conformance")
		if err := plan.ValidateInput(p, input); err != nil {
			resp.InputError = err.Error()
		}
	}

	// 3. Worker availability per executor.
	var liveWorkers []state.Worker
	registry, hasRegistry := store.(workerRegistry)
	if hasRegistry {
		window := WorkerHeartbeatWindow
		if coordinator != nil && coordinator.heartbeat > 0 {
			window = coordinator.heartbeat
		}
		since := time.Now().Add(-window)
		workers, err := registry.ListWorkers(since)
		if err == nil {
			liveWorkers = workers
			resp.Verified = append(resp.Verified, "worker_registry_snapshot", "executor_readiness_last_probe")
		} else {
			resp.WorkerError = err.Error()
		}
	}
	resp.WorkersChecked = len(liveWorkers)

	// Gather per-executor requirements from all execute nodes.
	execReqs := collectExecutorRequirements(p)

	allExecutorsSatisfiable := true
	for _, req := range execReqs {
		req.request.WorkspaceID, req.request.WorkspaceWorkerID = resp.WorkspaceID, resp.WorkspaceWorkerID
		ep := buildExecutorPreflight(req.request.Executor, req, liveWorkers)
		resp.Executors = append(resp.Executors, ep)
		if !ep.Satisfiable {
			allExecutorsSatisfiable = false
		}
	}

	resp.Feasible = !planHasErrors && resp.InputError == "" && allExecutorsSatisfiable && resp.WorkspaceError == "" && resp.WorkerError == ""
	return resp
}

// buildExecutorPreflight checks whether any live worker can satisfy the
// given executor requirements and returns a populated ExecutorPreflight.
func buildExecutorPreflight(execName string, req *executorRequirements, liveWorkers []state.Worker) ExecutorPreflight {
	ep := ExecutorPreflight{
		Executor: execName,
		NodeIDs:  req.nodeIDs,
		Subgraph: req.subgraph,
		Constraints: ExecutorConstraints{
			WorkspaceMode:     req.request.WorkspaceMode,
			ExecutorVersion:   req.executorVersion,
			IsolationRequired: req.isolationRequired,
			IsolationRuntime:  req.isolationRuntime,
			WorkspaceRequired: req.workspaceRequired,
		},
	}
	if len(req.requiredCapabilities) > 0 {
		ep.Constraints.RequiredCapabilities = req.requiredCapabilities
	}
	if len(req.placementLabels) > 0 {
		ep.Constraints.PlacementLabels = req.placementLabels
	}

	guarded, guardErr := workers.GuardRequest(req.request)
	if guardErr == nil {
		guardErr = workers.ValidatePermissions(guarded)
	}
	ep.Policy = guarded.Policy
	if guardErr != nil {
		ep.BlockingReasons = []string{"incompatible_execution_policy"}
		ep.Diagnostics = []string{guardErr.Error()}
		return ep
	}
	if len(liveWorkers) == 0 {
		ep.BlockingReasons = []string{"no_live_workers"}
		ep.Diagnostics = []string{"Register a worker and confirm its heartbeat before submitting this plan."}
		return ep
	}
	raw, err := json.Marshal(guarded)
	if err != nil {
		ep.BlockingReasons = []string{"invalid_execution_request"}
		return ep
	}
	reasons := make(map[string]struct{})
	for _, w := range liveWorkers {
		for _, capability := range w.Capabilities {
			if capability.Name == execName {
				ep.WorkerEvidence = append(ep.WorkerEvidence, PreflightWorkerEvidence{WorkerID: w.ID, LastSeen: w.LastSeen, Capability: capability})
			}
		}
		if !w.Draining && w.Capacity > 0 && workers.MatchWorkerDispatch(w.ID, execName, raw, w.Capabilities, w.Labels) {
			ep.MatchingWorkers = append(ep.MatchingWorkers, w.ID)
		} else {
			collectBlockingReasons(execName, req, w, &reasons)
			if w.Draining {
				reasons["worker_draining"] = struct{}{}
			}
			if w.Capacity <= 0 {
				reasons["worker_capacity_disabled"] = struct{}{}
			}
			if guarded.WorkspaceID != "" && guarded.WorkspaceMode != "empty" && (guarded.WorkspaceWorkerID != w.ID || w.Labels[workers.WorkspaceIdentityLabelPrefix+guarded.WorkspaceID] != "true") {
				reasons["workspace_owner_mismatch"] = struct{}{}
			}
			reasons["dispatch_constraints_unsatisfied"] = struct{}{}
		}
	}

	sort.Strings(ep.MatchingWorkers)
	sort.Slice(ep.WorkerEvidence, func(i, j int) bool { return ep.WorkerEvidence[i].WorkerID < ep.WorkerEvidence[j].WorkerID })
	ep.Satisfiable = len(ep.MatchingWorkers) > 0
	if !ep.Satisfiable {
		for r := range reasons {
			ep.BlockingReasons = append(ep.BlockingReasons, r)
		}
		sort.Strings(ep.BlockingReasons)
		for _, reason := range ep.BlockingReasons {
			ep.Diagnostics = append(ep.Diagnostics, preflightReasonMessage(reason))
		}
	}
	return ep
}

func preflightReasonMessage(reason string) string {
	switch reason {
	case "executor_not_registered":
		return "Install and register the requested executor on an eligible worker."
	case "executor_not_ready":
		return "Resolve the failed or missing readiness checks in workerEvidence; run muxlio doctor on the worker host."
	case "executor_version_mismatch":
		return "Deploy the pinned executor version or update the plan's version requirement."
	case "missing_required_capability":
		return "Use an executor contract that supports the node's required capabilities and versions."
	case "isolation_runtime_unavailable":
		return "Enable the required isolation runtime on an eligible worker."
	case "placement_label_mismatch":
		return "Register a worker with the required placement labels or adjust the node's placement."
	case "workspace_owner_mismatch":
		return "The eligible worker must own and advertise the selected workspace identity."
	case "worker_draining":
		return "A draining worker cannot accept new work; select an active worker."
	case "worker_capacity_disabled":
		return "Enable positive worker capacity before submitting work."
	default:
		return "No worker satisfies the complete dispatch contract; check workspace mode, isolation configuration, environment pins, and session ownership."
	}
}

// collectBlockingReasons inspects why a specific worker does not match the
// executor and records the reason classes in the provided set.
func collectBlockingReasons(execName string, req *executorRequirements, w state.Worker, reasons *map[string]struct{}) {
	// Find the capability entry for this executor.
	var cap *state.WorkerCapability
	for i := range w.Capabilities {
		if w.Capabilities[i].Name == execName {
			cap = &w.Capabilities[i]
			break
		}
	}
	if cap == nil {
		(*reasons)["executor_not_registered"] = struct{}{}
		return
	}

	// Readiness check.
	if cap.Readiness != nil {
		if !cap.Readiness.Ready || len(cap.Readiness.Checks) == 0 || len(cap.Readiness.Failed()) > 0 {
			(*reasons)["executor_not_ready"] = struct{}{}
		}
	} else if cap.Contract != nil && cap.Contract.ProbeVersion != "" {
		(*reasons)["executor_not_ready"] = struct{}{}
	}

	// Executor version pin.
	if req.executorVersion != "" && cap.Version != req.executorVersion {
		(*reasons)["executor_version_mismatch"] = struct{}{}
	}

	// Required capabilities vs adapter manifest.
	if len(req.requiredCapabilities) > 0 {
		descriptor := cap.Contract
		if descriptor == nil {
			(*reasons)["missing_required_capability"] = struct{}{}
		} else {
			for capName, capVer := range req.requiredCapabilities {
				if !descriptor.Supports(capName) {
					(*reasons)["missing_required_capability"] = struct{}{}
					break
				}
				if capVer != "" {
					actual := descriptor.CapabilityVersions[capName]
					if actual == "" {
						actual = "1"
					}
					if actual != capVer {
						(*reasons)["missing_required_capability"] = struct{}{}
						break
					}
				}
			}
		}
	}

	// Isolation runtime label.
	if req.isolationRequired && req.isolationRuntime != "" {
		label := workers.IsolationLabelPrefix + req.isolationRuntime
		if w.Labels[label] != "true" {
			(*reasons)["isolation_runtime_unavailable"] = struct{}{}
		}
	}

	// Placement labels.
	for k, v := range req.placementLabels {
		if w.Labels[k] != v {
			(*reasons)["placement_label_mismatch"] = struct{}{}
			break
		}
	}
}

// unverifiedItems lists aspects that cannot be confirmed without executing.
var unverifiedItems = []string{
	"current_provider_authentication",
	"remote_quota_and_rate_limits",
	"fine_grained_tool_permissions",
	"worker_operator_tool_permission_policy",
	"readiness_checks_not_reported_by_worker",
	"concurrent_admission_capacity",
	"workspace_path_accessibility",
	"network_connectivity_to_provider",
	"output_schema_conformance",
}

// handlePreflightRun serves POST /v1/runs/preflight.
//
// It validates the plan, validates the effective input against the plan's
// input schema, checks whether live workers can satisfy every executor, and
// returns a structured PreflightResponse. No run is created.
func handlePreflightRun(w http.ResponseWriter, r *http.Request, store state.Store, runner Runner) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, CodeMethodNotAllowed, "use POST")
		return
	}

	var req PreflightRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		writeError(w, 400, CodeInvalidJSON, err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, 400, CodeInvalidJSON, "request body must contain one JSON object")
		return
	}

	var resp PreflightResponse
	if validator, ok := runner.(interface {
		PreflightRun(PreflightRequest) PreflightResponse
	}); ok {
		resp = validator.PreflightRun(req)
	} else {
		resp = PreflightRun(&req.Plan, req.Input, store)
		if req.WorkspaceID != "" || req.Plan.WorkspaceID != "" {
			resp.Feasible = false
			resp.WorkspaceError = "workspace validation is unavailable on this runner"
		}
	}
	writeJSON(w, 200, resp)
}

// PreflightRun validates workspace authority without creating runs or invoking tools.
func (c *Coordinator) PreflightRun(req PreflightRequest) PreflightResponse {
	p := req.Plan
	if err := p.VerifyDigest(); err != nil {
		resp := preflightRun(&p, req.Input, c.store, nil)
		resp.Feasible = false
		resp.WorkspaceError = err.Error()
		return resp
	}
	if req.WorkspaceID != "" {
		if p.WorkspaceID != "" {
			original, _, e1 := c.workspaceRegistry().Resolve(p.WorkspaceID)
			selected, _, e2 := c.workspaceRegistry().Resolve(req.WorkspaceID)
			if e1 != nil || e2 != nil || original != selected {
				resp := preflightRun(&p, req.Input, c.store, nil)
				resp.Feasible = false
				resp.WorkspaceError = "submission and plan workspace IDs disagree or are unregistered; select an operator-configured workspace ID"
				return resp
			}
		}
		p.WorkspaceID = req.WorkspaceID
		_, _ = p.ComputeDigest()
	}
	return preflightRun(&p, req.Input, c.store, c)
}
