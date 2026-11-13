package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// Variants preserve descriptor differences rather than unioning features from
// incompatible installations. Eligibility is always checked against one worker.
type executorVariant struct {
	Version               string                    `json:"executorVersion,omitempty"`
	Contract              *contract.AdapterManifest `json:"contract,omitempty"`
	CapabilityVersions    map[string]string         `json:"capabilityVersions"`
	Operations            []string                  `json:"operations"`
	RegisteredWorkerCount int                       `json:"registeredWorkerCount"`
	EligibleWorkerCount   int                       `json:"eligibleWorkerCount"`
	IsolationWorkerCounts map[string]int            `json:"isolationWorkerCounts"`
}

type executorChoice struct {
	Executor             string             `json:"executor"`
	EligibleWorkerCount  int                `json:"eligibleWorkerCount"`
	AvailableWorkerCount int                `json:"availableWorkerCount"`
	QueueReason          string             `json:"queueReason"`
	BlockingReasons      []string           `json:"blockingReasons,omitempty"`
	Variants             []*executorVariant `json:"variants"`
	Workers              []executorEvidence `json:"workers,omitempty"`
}

type executorEvidence struct {
	WorkerID  string                    `json:"workerId"`
	Version   string                    `json:"executorVersion,omitempty"`
	LastSeen  time.Time                 `json:"lastSeen"`
	Readiness *contract.ProbeResult     `json:"readiness,omitempty"`
	Labels    map[string]string         `json:"labels,omitempty"`
	Eligible  bool                      `json:"eligible"`
	Available bool                      `json:"available"`
	Reason    *workers.DispatchMismatch `json:"reason,omitempty"`
}

func executorDiscoveryTools() []toolDef {
	requestProperties := map[string]any{}
	for _, key := range []string{"executorVersion", "contractVersion", "workspaceId", "workspaceWorkerId", "workspaceMode", "sessionId", "effect", "idempotencyKey"} {
		requestProperties[key] = map[string]any{"type": "string"}
	}
	for _, key := range []string{"requiredCapabilities", "placementLabels"} {
		requestProperties[key] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	}
	for _, key := range []string{"environment", "session", "remoteTask", "outputs", "params", "executionPolicy"} {
		requestProperties[key] = map[string]any{"type": "object"}
	}
	requestProperties["action"] = map[string]any{"type": "string", "enum": []string{"", "poll", "reconcile", "steer"}}
	requestProperties["sessionInputs"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
	properties := map[string]any{
		"executor": map[string]any{"type": "string", "minLength": 1, "description": "Exact registered executor name; required for explain_capability."},
		"request":  map[string]any{"type": "object", "properties": requestProperties, "description": "Shared execute envelope constraints: executorVersion, contractVersion, requiredCapabilities, environment, action, session/sessionId, remoteTask, outputs, placementLabels, workspaceId/workspaceWorkerId/workspaceMode, params.isolation, executionPolicy. No execution occurs. Use preflight_run for authorized workspace and whole-plan validation."},
	}
	return []toolDef{
		{Name: "list_executors", Description: "Small catalog of live registered executors, separate descriptor variants, declared capability versions and operations, eligible worker counts, estimated worker availability and queue reason (one slot per active assignment; weighted admission is unverified). Optionally filter by executor and shared request constraints. Cached readiness and declarations do not guarantee provider functionality, usage availability, or admission.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": properties}},
		{Name: "explain_capability", Description: "Explain whether one executor satisfies a shared execute request, with per-worker machine-readable rejection reasons, readiness evidence, environment and isolation constraints. Use requiredCapabilities with exact versions; absent features are unsupported. No provider calls or capacity reservation.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": []string{"executor"}}},
	}
}

func (s *server) toolExecutorDiscovery(name string, args json.RawMessage) toolCallResult {
	for _, tool := range executorDiscoveryTools() {
		if tool.Name == name {
			schema, _ := json.Marshal(tool.InputSchema)
			if err := plan.ValidateInput(&plan.Plan{InputSchema: schema}, args); err != nil {
				return errResult("invalid arguments: " + err.Error())
			}
			break
		}
	}

	var params struct {
		Executor string                 `json:"executor"`
		Request  workers.ExecuteRequest `json:"request"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errResult("arguments must contain one JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(args), []byte("null")) {
		return errResult("arguments must be an object")
	}
	if name == "explain_capability" && params.Executor == "" {
		return errResult("executor is required")
	}
	if params.Request.Executor != "" && params.Request.Executor != params.Executor {
		return errResult("request.executor must match executor")
	}
	resp, err := s.api.get("/v1/workers", nil)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	if resp.statusCode < 200 || resp.statusCode >= 300 {
		return apiResultOrError(resp)
	}
	var snapshot struct {
		Registered             []state.Worker     `json:"registeredWorkers"`
		Assignments            []state.Assignment `json:"workers"`
		ServerTime             time.Time          `json:"serverTime"`
		HeartbeatWindowSeconds float64            `json:"heartbeatWindowSeconds"`
	}
	if err := contract.DecodeJSON(resp.body, &snapshot); err != nil {
		return errResult("invalid worker snapshot: " + err.Error())
	}
	choices := buildExecutorCatalog(snapshot.Registered, snapshot.Assignments, params.Executor, params.Request, name == "explain_capability")
	result := map[string]any{
		"executors": choices, "serverTime": snapshot.ServerTime, "heartbeatWindowSeconds": snapshot.HeartbeatWindowSeconds,
		"evidence": "worker registrations, adapter descriptors and cached readiness; no live provider verification",
		"capacityEstimate": map[string]any{
			"basis": "active_assignment_count", "assumedSlotsPerAssignment": 1, "weightedAdmissionVerified": false,
		},
		"unverified": []string{"weighted_worker_capacity", "provider_functionality", "current_authentication", "remote_quota", "usage_readings", "fine_grained_tool_permissions", "worker_operator_policy", "global_run_key_rate_and_resource_admission", "workspace_authorization"},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return errResult(err.Error())
	}
	return toolCallResult{Content: []contentBlock{{Type: "text", Text: string(raw)}}}
}

func buildExecutorCatalog(registered []state.Worker, assignments []state.Assignment, filter string, request workers.ExecuteRequest, explain bool) []*executorChoice {
	// The worker snapshot has no admission reservations or executor slot costs.
	// Availability and queue reasons therefore assume unit-cost assignments; the
	// response exposes this estimate separately from verified dispatch matching.
	active := map[string]int{}
	for _, a := range assignments {
		if a.State == "active" {
			active[a.WorkerID]++
		}
	}
	names := map[string]bool{}
	for _, w := range registered {
		for _, c := range w.Capabilities {
			if filter == "" || c.Name == filter {
				names[c.Name] = true
			}
		}
	}
	if filter != "" {
		names[filter] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	out := make([]*executorChoice, 0, len(sorted))
	for _, name := range sorted {
		choice := &executorChoice{Executor: name, Variants: []*executorVariant{}}
		variants := map[string]*executorVariant{}
		request.Executor = name
		guarded, guardErr := workers.GuardRequest(request)
		raw, err := json.Marshal(guarded)
		reasons := map[string]bool{}
		for _, w := range registered {
			var capability *state.WorkerCapability
			for i := range w.Capabilities {
				if w.Capabilities[i].Name == name {
					capability = &w.Capabilities[i]
					break
				}
			}
			if capability == nil {
				continue
			}
			keyBytes, _ := json.Marshal([]any{capability.Version, capability.Contract})
			key := string(keyBytes)
			variant := variants[key]
			if variant == nil {
				variant = &executorVariant{Version: capability.Version, Contract: capability.Contract, CapabilityVersions: map[string]string{}, Operations: []string{}, IsolationWorkerCounts: map[string]int{}}
				m := capability.Contract
				if m == nil {
					m = &contract.AdapterManifest{ExecutorVersion: capability.Version}
				}
				for feature, supported := range m.Capabilities {
					if supported {
						version := m.CapabilityVersions[feature]
						if version == "" {
							version = contract.Version
						}
						variant.CapabilityVersions[feature] = version
					}
				}
				variant.Operations = m.SupportedOperations(name)
				variants[key] = variant
			}
			variant.RegisteredWorkerCount++
			for label, enabled := range w.Labels {
				if enabled == "true" && strings.HasPrefix(label, workers.IsolationLabelPrefix) {
					variant.IsolationWorkerCounts[strings.TrimPrefix(label, workers.IsolationLabelPrefix)]++
				}
			}
			reason := workers.ExplainWorkerDispatch(w.ID, name, raw, w.Capabilities, w.Labels)
			if guardErr != nil {
				reason = &workers.DispatchMismatch{Code: "incompatible_execution_policy", Message: guardErr.Error()}
			}
			if err != nil {
				reason = &workers.DispatchMismatch{Code: "invalid_execution_request", Message: err.Error()}
			}
			if reason == nil && w.Draining {
				reason = &workers.DispatchMismatch{Code: "worker_draining", Message: "Select a worker accepting new work."}
			}
			if reason == nil && w.Capacity <= 0 {
				reason = &workers.DispatchMismatch{Code: "worker_capacity_disabled", Message: "Enable positive worker capacity."}
			}
			eligible := reason == nil
			available := eligible && active[w.ID] < w.Capacity
			if eligible {
				choice.EligibleWorkerCount++
				variant.EligibleWorkerCount++
			}
			if available {
				choice.AvailableWorkerCount++
			}
			if eligible && !available {
				reason = &workers.DispatchMismatch{Code: "worker_full", Message: "Wait for worker capacity or register another eligible worker."}
			}
			if reason != nil {
				reasons[reason.Code] = true
			}
			if explain {
				choice.Workers = append(choice.Workers, executorEvidence{WorkerID: w.ID, Version: capability.Version, LastSeen: w.LastSeen, Readiness: capability.Readiness, Labels: w.Labels, Eligible: eligible, Available: available, Reason: reason})
			}
		}
		keys := make([]string, 0, len(variants))
		for key := range variants {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			choice.Variants = append(choice.Variants, variants[key])
		}
		sort.Slice(choice.Workers, func(i, j int) bool { return choice.Workers[i].WorkerID < choice.Workers[j].WorkerID })
		if choice.EligibleWorkerCount == 0 {
			choice.QueueReason = "no_eligible_workers"
		} else if choice.AvailableWorkerCount == 0 {
			choice.QueueReason = "worker_full"
		}
		if choice.EligibleWorkerCount == 0 {
			if len(variants) == 0 {
				reasons["executor_not_registered"] = true
			}
			for reason := range reasons {
				choice.BlockingReasons = append(choice.BlockingReasons, reason)
			}
			sort.Strings(choice.BlockingReasons)
		}
		out = append(out, choice)
	}
	return out
}
