package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/sandbox"
	"github.com/muxlio/muxlio-core/internal/state"
)

// ExecutorSelector resolves a workflow selector before an attempt is persisted.
// The load snapshot is a preference, never an additional dispatch requirement.
type ExecutorSelector interface {
	SelectExecutor(ExecuteRequest, map[string]int) (string, error)
}

// AdmissionSelector resolves an agent atomically with durable capacity admission.
type AdmissionSelector interface{ SelectAtAdmission() bool }

// AgentSelectionPreferences are scheduling hints, never permission grants.
type AgentSelectionPreferences struct {
	FailedExecutors    []string         `json:"failedExecutors,omitempty"`
	LastFailedExecutor string           `json:"lastFailedExecutor,omitempty"`
	PreviousExecutors  []string         `json:"previousExecutors,omitempty"`
	LastUsed           map[string]int64 `json:"lastUsed,omitempty"`
	HeartbeatSeconds   float64          `json:"heartbeatSeconds,omitempty"`
}

type AgentCandidate struct {
	Executor, WorkerID   string
	Available            bool
	Load                 int
	WorkerLoad, Capacity int
	LastTurn             int64
}

// ChooseAgent prioritizes a fresh agent after failure, then immediate admission,
// dependency diversity, current load, and least-recently-selected rotation.
func ChooseAgent(candidates []AgentCandidate, preferences *AgentSelectionPreferences) (AgentCandidate, error) {
	failed, previous := map[string]bool{}, map[string]bool{}
	if preferences != nil {
		for _, name := range preferences.FailedExecutors {
			failed[name] = true
		}
		for _, name := range preferences.PreviousExecutors {
			previous[name] = true
		}
	}
	fresh := false
	for _, c := range candidates {
		if !failed[c.Executor] {
			fresh = true
		}
	}
	choices := make([]AgentCandidate, 0, len(candidates))
	lastFailed := ""
	if preferences != nil {
		lastFailed = preferences.LastFailedExecutor
	}
	alternative := false
	for _, c := range candidates {
		if c.Executor != lastFailed {
			alternative = true
		}
	}
	for _, c := range candidates {
		if (fresh && !failed[c.Executor]) || (!fresh && (lastFailed == "" || !alternative || c.Executor != lastFailed)) {
			choices = append(choices, c)
		}
	}
	if len(choices) == 0 {
		return AgentCandidate{}, fmt.Errorf("no available compatible agent satisfies the execution requirements")
	}
	sort.Slice(choices, func(i, j int) bool {
		a, b := choices[i], choices[j]
		if a.Available != b.Available {
			return a.Available
		}
		if previous[a.Executor] != previous[b.Executor] {
			return !previous[a.Executor]
		}
		if a.Load != b.Load {
			return a.Load < b.Load
		}
		if a.WorkerLoad*b.Capacity != b.WorkerLoad*a.Capacity {
			return a.WorkerLoad*b.Capacity < b.WorkerLoad*a.Capacity
		}
		if a.LastTurn != b.LastTurn {
			return a.LastTurn < b.LastTurn
		}
		if a.Executor != b.Executor {
			return a.Executor < b.Executor
		}
		return a.WorkerID < b.WorkerID
	})
	return choices[0], nil
}

func IsAnyAgent(name string) bool { return name == "any" || name == "agent/any" }

func IsAgent(name string) bool {
	return strings.HasPrefix(name, "agent/") && !IsAnyAgent(name)
}

// GuardSelectionRequest validates declared security before a concrete agent is
// known. Actual selection still guards every candidate against the full policy.
func GuardSelectionRequest(req ExecuteRequest) (ExecuteRequest, error) {
	if !IsAnyAgent(req.Executor) {
		return GuardRequest(req)
	}
	selector := req.Executor
	configured, err := executionSecurityConfig(req)
	if err != nil {
		return ExecuteRequest{}, err
	}
	req = configured
	if req.Session != nil && req.Session.Mode == "resume" && req.Session.Handle != nil {
		req.Executor = req.Session.Handle.Executor
	} else if req.Policy != nil && req.Policy.Required {
		for _, name := range req.Policy.AllowedCapabilities {
			if IsAgent(name) {
				req.Executor = name
				break
			}
		}
		if IsAnyAgent(req.Executor) {
			return ExecuteRequest{}, fmt.Errorf("execution policy must allow at least one concrete agent")
		}
	}
	guarded, err := GuardRequest(req)
	guarded.Executor = selector
	return guarded, err
}

// CompatibleAgents applies the same policy and dispatch checks as concrete work.
func CompatibleAgents(req ExecuteRequest, worker state.Worker) []string {
	var names []string
	for _, capability := range worker.Capabilities {
		if !IsAgent(capability.Name) {
			continue
		}
		candidate := req
		candidate.Executor = capability.Name
		guarded, err := GuardRequest(candidate)
		if err != nil {
			continue
		}
		raw, err := json.Marshal(guarded)
		if err == nil && MatchWorkerDispatch(worker.ID, candidate.Executor, raw, worker.Capabilities, worker.Labels) {
			names = append(names, candidate.Executor)
		}
	}
	return names
}

// SelectAgent ranks immediately available candidates by current load.
func SelectAgent(candidates []string, usage map[string]int) (string, error) {
	return selectLocalAgent(candidates, usage, nil)
}

func selectLocalAgent(names []string, load map[string]int, preferences *AgentSelectionPreferences) (string, error) {
	var candidates []AgentCandidate
	for _, name := range names {
		c := AgentCandidate{Executor: name, Available: true, Load: load[name], Capacity: 1}
		if preferences != nil {
			c.LastTurn = preferences.LastUsed[name]
		}
		candidates = append(candidates, c)
	}
	choice, err := ChooseAgent(candidates, preferences)
	return choice.Executor, err
}

func (s *ScriptExecutor) SelectExecutor(req ExecuteRequest, usage map[string]int) (string, error) {
	if !IsAnyAgent(req.Executor) {
		return req.Executor, nil
	}
	var err error
	req, err = GuardSelectionRequest(req)
	if err != nil {
		return "", err
	}
	req, err = ApplyAuthorizedWorkspace(req, s.WorkerID, s.Workspaces, s.Workspace)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(s.adapterDir)
	if err != nil {
		return "", err
	}
	worker := state.Worker{ID: s.WorkerID, Labels: map[string]string{SessionEnvironmentLabel: s.SessionEnvironment()}}
	var params struct {
		Isolation *isolationPolicy `json:"isolation"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return "", err
		}
	}
	if policy := params.Isolation; policy != nil && policy.Mode != "trusted-native" {
		if policy.Mode != "required" {
			return "", fmt.Errorf("isolation mode must be required or trusted-native")
		}
		labels, err := IsolationLabels([]string{policy.Runtime})
		if err != nil {
			return "", err
		}
		for key, value := range labels {
			worker.Labels[key] = value
		}
	}
	if s.Workspace != "" {
		worker.Labels[WorkspacePlacementLabel] = s.Workspace
	}
	for id, binding := range s.Workspaces {
		if binding.WorkerID == s.WorkerID {
			worker.Labels[WorkspaceIdentityLabelPrefix+id] = "true"
			worker.Labels[WorkspaceDigestLabelPrefix+id] = binding.Digest
		}
	}
	ctx := req.Context
	if ctx == nil {
		ctx = s.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, entry := range entries {
		name := strings.Replace(entry.Name(), "-", "/", 1)
		if !IsAgent(name) || entry.IsDir() || strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&0111 == 0 {
			continue
		}
		descriptor, err := s.Descriptor(name)
		if err != nil {
			continue
		}
		candidate := req
		candidate.Executor = name
		candidate, err = GuardRequest(candidate)
		if err != nil {
			continue
		}
		readiness := s.probeSelection(ctx, candidate)
		worker.Capabilities = append(worker.Capabilities, state.WorkerCapability{Name: name, Version: descriptor.ExecutorVersion, Contract: descriptor, Readiness: &readiness})
	}
	return selectLocalAgent(CompatibleAgents(req, worker), usage, req.AgentSelection)
}

// Selection probes use the task's launch path, including isolation and operator
// authorization, before any workflow attempt is admitted. They receive no task
// input or secrets and allocate a separate scratch directory for the invocation.
func (s *ScriptExecutor) probeSelection(ctx context.Context, req ExecuteRequest) contract.ProbeResult {
	return s.probe(ctx, req.Executor, func() (sandbox.Launch, error) {
		scratch, err := newAttemptScratch(ExecuteRequest{}, s.workBase)
		if err != nil {
			return sandbox.Launch{}, err
		}
		// Selection precedes resource admission. A readiness probe must never
		// receive the task's writable workspace before its locks are acquired.
		req.Workspace = ""
		launch, err := s.launch(req, s.scriptPath(req.Executor), scratch, "--muxlio-probe")
		if err != nil {
			_ = os.RemoveAll(scratch)
			return sandbox.Launch{}, err
		}
		cleanup := launch.Cleanup
		launch.Cleanup = func() error {
			if cleanup != nil {
				if err := cleanup(); err != nil {
					return err
				}
			}
			return os.RemoveAll(scratch)
		}
		return launch, nil
	})
}

func (f *FakeExecutor) SelectExecutor(req ExecuteRequest, usage map[string]int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.byExec {
		if IsAgent(name) {
			names = append(names, name)
		}
	}
	return selectLocalAgent(names, usage, req.AgentSelection)
}
