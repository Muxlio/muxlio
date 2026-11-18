package mcp

import (
	"bytes"
	"encoding/json"
	"net/url"
)

func namedWorkflowTools() []toolDef {
	id := map[string]any{"type": "string", "pattern": `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`, "description": "Stable caller-assigned workflow ID."}
	plan := map[string]any{"type": "object", "description": "Compiled Plan IR document."}
	return []toolDef{
		{
			Name: "create_workflow", Description: "Publish the initial version of a named workflow on the coordinator.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"workflowId": id, "plan": plan},
				"required":   []string{"workflowId", "plan"},
			},
		},
		{
			Name: "get_workflow", Description: "Inspect a workflow's current revision, current digest, and version history. Pass the digest as planDigest to start_run.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"workflowId": id},
				"required":   []string{"workflowId"},
			},
		},
		{
			Name: "update_workflow", Description: "Publish a new immutable version or select a previously published digest using the current revision from get_workflow.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"workflowId": id, "plan": plan,
					"expectedCurrentRevision": map[string]any{"type": "integer", "minimum": 1},
					"planDigest":              map[string]any{"type": "string", "minLength": 1, "description": "Select an eligible version already published to this workflow."},
				},
				"required": []string{"workflowId", "expectedCurrentRevision"},
				"oneOf": []any{
					map[string]any{"required": []string{"plan"}, "not": map[string]any{"required": []string{"planDigest"}}},
					map[string]any{"required": []string{"planDigest"}, "not": map[string]any{"required": []string{"plan"}}},
				},
			},
		},
	}
}

func (s *server) toolNamedWorkflow(tool string, args json.RawMessage) toolCallResult {
	var params struct {
		WorkflowID              string          `json:"workflowId"`
		Plan                    json.RawMessage `json:"plan,omitempty"`
		PlanDigest              string          `json:"planDigest,omitempty"`
		ExpectedCurrentRevision int64           `json:"expectedCurrentRevision,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if params.WorkflowID == "" {
		return errResult("workflowId is required")
	}

	path := "/v1/workflows"
	var resp *apiResponse
	var err error
	switch tool {
	case "create_workflow":
		if params.PlanDigest != "" || params.ExpectedCurrentRevision != 0 {
			return errResult("create_workflow accepts workflowId and plan")
		}
		resp, err = s.api.post(path, params)
	case "get_workflow":
		if len(params.Plan) > 0 || params.PlanDigest != "" || params.ExpectedCurrentRevision != 0 {
			return errResult("get_workflow accepts workflowId")
		}
		resp, err = s.api.get(path+"/"+url.PathEscape(params.WorkflowID), nil)
	case "update_workflow":
		resp, err = s.api.post(path+"/"+url.PathEscape(params.WorkflowID), map[string]any{
			"expectedCurrentRevision": params.ExpectedCurrentRevision,
			"plan":                    params.Plan,
			"planDigest":              params.PlanDigest,
		})
	}
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}
