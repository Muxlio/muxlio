package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/muxlio/muxlio-core/internal/authoring"
	"github.com/muxlio/muxlio-core/internal/plan"
)

// Catalog calls are offline authoring operations and never start a run or
// register an immutable workflow version.
func (s *server) toolWorkflowCatalog(name string, args json.RawMessage) toolCallResult {
	for _, tool := range workflowCatalogTools() {
		if tool.Name == name {
			schema, _ := json.Marshal(tool.InputSchema)
			if err := plan.ValidateInput(&plan.Plan{InputSchema: schema}, args); err != nil {
				return errResult("invalid arguments: " + err.Error())
			}
			break
		}
	}
	var params struct {
		Name       string          `json:"name"`
		Version    string          `json:"version"`
		Parameters json.RawMessage `json:"parameters"`
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errResult("arguments must contain one JSON object")
	}
	var result any
	switch name {
	case "list_workflow_templates":
		if params.Name != "" || params.Version != "" || len(params.Parameters) != 0 {
			return errResult("list_workflow_templates accepts no parameters")
		}
		result = map[string]any{"templates": authoring.ListWorkflowTemplates()}
	case "get_workflow_template":
		if len(params.Parameters) != 0 {
			return errResult("get_workflow_template accepts name and version only")
		}
		t, err := authoring.GetWorkflowTemplate(params.Name, params.Version)
		if err != nil {
			return errResult(err.Error())
		}
		result = t
	case "instantiate_workflow_template":
		p, err := authoring.InstantiateWorkflowTemplate(params.Name, params.Version, params.Parameters)
		if err != nil {
			return errResult(err.Error())
		}
		result = map[string]any{"template": map[string]string{"name": params.Name, "version": params.Version}, "plan": p, "digest": p.Digest}
	default:
		return errResult(fmt.Sprintf("unknown catalog operation %q", name))
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return errResult(err.Error())
	}
	return toolCallResult{Content: []contentBlock{{Type: "text", Text: string(raw)}}}
}

func workflowCatalogTools() []toolDef {
	identity := map[string]any{"name": map[string]any{"type": "string", "minLength": 1}, "version": map[string]any{"type": "string", "minLength": 1}}
	return []toolDef{
		{Name: "list_workflow_templates", Description: "Discover reusable generic workflow shapes and their parameter, run input, and output JSON Schemas. Offline and read-only. Templates are authoring recipes, separate from pinned immutable workflow versions.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}},
		{Name: "get_workflow_template", Description: "Inspect a reusable template at an explicit catalog version. Use list_workflow_templates to discover names and versions.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": identity, "required": []string{"name", "version"}}},
		{Name: "instantiate_workflow_template", Description: "Validate template parameters and compile an ordinary editable Plan IR with its digest, inputSchema and outputSchema. No execution or registration. Use check_plan, preflight_run and start_run with separate run input, or register_workflow_version to pin the resulting definition. Inspect get_workers to choose executors.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"name": identity["name"], "version": identity["version"], "parameters": map[string]any{"type": "object"}}, "required": []string{"name", "version", "parameters"}}},
	}
}
