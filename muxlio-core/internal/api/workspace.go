package api

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// WorkspaceAuthorizationError reports submission preflight failures without
// creating a run or granting filesystem authority to an input path.
type WorkspaceAuthorizationError struct{ Message string }

func (e WorkspaceAuthorizationError) Error() string { return e.Message }
func (e WorkspaceAuthorizationError) ProtocolError() (int, string, string) {
	return 400, "MUXLIO_WORKSPACE_UNAUTHORIZED", e.Message
}

func (c *Coordinator) workspaceRegistry() workers.WorkspaceRegistry {
	if len(c.Workspaces) != 0 {
		return c.Workspaces
	}
	if c.Workspace != "" {
		return workers.WorkspaceRegistry{"default": {WorkerID: "local", Path: c.Workspace}}
	}
	return nil
}

// AuthorizePlanWorkspace pins the canonical identity into the immutable plan
// stored with the run. Aliases never produce a second run workspace identity.
func (c *Coordinator) AuthorizePlanWorkspace(p *plan.Plan, input json.RawMessage) error {
	if err := p.VerifyDigest(); err != nil {
		return err
	}
	registry := c.workspaceRegistry()
	id := p.WorkspaceID
	if id == "" && len(c.Workspaces) == 0 && c.Workspace != "" {
		id = "default"
	}
	needsWorkspace := false
	for _, graph := range p.Subgraphs {
		for _, node := range graph.Nodes {
			if node.Kind == "execute" && node.WorkspaceMode != "empty" {
				needsWorkspace = true
			}
		}
	}
	if !needsWorkspace {
		if id != "" {
			canonical, _, err := registry.Resolve(id)
			if err != nil {
				return WorkspaceAuthorizationError{err.Error()}
			}
			if p.WorkspaceID != canonical {
				p.WorkspaceID = canonical
				_, err = p.ComputeDigest()
				return err
			}
		}
		return nil
	}
	var inputs map[string]any
	_ = json.Unmarshal(input, &inputs)
	req, err := c.authorizeWorkspace(workers.ExecuteRequest{WorkspaceID: id, WorkspaceDigest: p.WorkspaceDigest, Inputs: inputs})
	if err != nil {
		return WorkspaceAuthorizationError{err.Error()}
	}
	// Resolve statically known workspace inputs before creating the run. Ports
	// that depend on earlier outputs are checked again before dispatch.
	for _, graph := range p.Subgraphs {
		for _, node := range graph.Nodes {
			binding, ok := node.Inputs["workspace"]
			if !ok || node.Kind != "execute" || node.WorkspaceMode == "empty" {
				continue
			}
			value, resolveErr := binding.Resolve(&plan.EvalContext{Input: inputs})
			if resolveErr != nil {
				continue
			}
			if _, err := c.authorizeWorkspace(workers.ExecuteRequest{WorkspaceID: req.WorkspaceID, WorkspaceDigest: p.WorkspaceDigest, Inputs: map[string]any{"workspace": value}}); err != nil {
				return WorkspaceAuthorizationError{err.Error()}
			}
		}
	}
	if p.WorkspaceID != req.WorkspaceID {
		p.WorkspaceID = req.WorkspaceID
		_, err = p.ComputeDigest()
	}
	return err
}

func (c *Coordinator) authorizeWorkspace(req workers.ExecuteRequest) (workers.ExecuteRequest, error) {
	registry := c.workspaceRegistry()
	if req.WorkspaceID == "" && len(c.Workspaces) == 0 && c.Workspace != "" {
		req.WorkspaceID = "default"
	}
	if req.WorkspaceMode == "empty" || req.Action == "poll" || req.Action == "reconcile" {
		return req, nil
	}
	if req.WorkspaceDigest != "" && req.WorkspaceID == "" {
		return req, fmt.Errorf("workspaceDigest requires an explicit workspaceId")
	}
	if req.WorkspaceID == "" && len(registry) == 0 {
		return req, workers.CheckWorkspaceInput(req.Inputs, "", false)
	}
	id, binding, err := registry.Resolve(req.WorkspaceID)
	if err != nil {
		return req, err
	}
	if err := workers.ValidateWorkspaceDigest(req.WorkspaceDigest); err != nil {
		return req, err
	}
	if req.WorkspaceDigest != binding.Digest {
		return req, fmt.Errorf("workspace %q prepared digest does not match the plan; pin workspaceDigest and provision matching contents", id)
	}
	req.WorkspaceID, req.WorkspaceWorkerID = id, binding.WorkerID
	if binding.WorkerID == "local" {
		// Validate input path aliases without shipping coordinator paths to a
		// different host. The worker independently repeats this preflight.
		if _, err := workers.ApplyAuthorizedWorkspace(req, "local", registry, ""); err != nil {
			return req, err
		}
		return req, nil
	}
	if err := workers.CheckWorkspaceInput(req.Inputs, binding.Path, false); err != nil {
		return req, err
	}
	if c.store != nil {
		available, err := c.store.ListWorkers(time.Now().Add(-c.heartbeat))
		if err != nil {
			return req, err
		}
		for _, worker := range available {
			if worker.ID == binding.WorkerID && worker.Labels[workers.WorkspaceIdentityLabelPrefix+id] == "true" && (req.WorkspaceDigest == "" || worker.Labels[workers.WorkspaceDigestLabelPrefix+id] == req.WorkspaceDigest) {
				return req, nil
			}
		}
	}
	return req, fmt.Errorf("workspace %q has no available owning worker %q advertising the requested content revision; no checkout substitution is allowed", id, binding.WorkerID)
}

// ResolveRunWorkspaceID resolves current aliases for discovery. Unknown selectors
// remain exact metadata filters so retained runs outlive their execution bindings.
// It grants no filesystem authority and performs no execution.
func (c *Coordinator) ResolveRunWorkspaceID(id string) (string, error) {
	registry := c.workspaceRegistry()
	if err := registry.Validate(); err != nil {
		return "", err
	}
	if canonical, _, err := registry.Resolve(id); err == nil {
		return canonical, nil
	}
	return id, nil
}
