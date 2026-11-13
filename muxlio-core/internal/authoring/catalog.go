package authoring

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/muxlio/muxlio-core/internal/plan"
)

// WorkflowTemplate describes an authoring recipe, not a registered immutable plan.
// Parameters select the shape and executors; run inputs remain separate.
type WorkflowTemplate struct {
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	Description     string          `json:"description"`
	ParameterSchema json.RawMessage `json:"parameterSchema"`
	InputSchema     json.RawMessage `json:"inputSchema"`
	OutputSchema    json.RawMessage `json:"outputSchema"`
}

type templateStep struct {
	Executor string          `json:"executor"`
	Task     string          `json:"task,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Output   plan.OutputDecl `json:"output,omitempty"`
}

type templateParameters struct {
	Name                  string          `json:"name"`
	Work                  templateStep    `json:"work"`
	Verify                templateStep    `json:"verify"`
	Repair                templateStep    `json:"repair"`
	Review                templateStep    `json:"review"`
	Steps                 []templateStep  `json:"steps"`
	Concurrency           int             `json:"concurrency"`
	MaxItems              int             `json:"maxItems"`
	MaxIterations         int             `json:"maxIterations"`
	RequestName           string          `json:"requestName"`
	RequestSchema         json.RawMessage `json:"requestSchema"`
	RequestExpiresSeconds *float64        `json:"requestExpiresSeconds"`
	RequestPermissions    []string        `json:"requestPermissions"`
}

func schemaJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func approvalSchema(verified bool) map[string]any {
	approved := map[string]any{"type": "boolean"}
	if verified {
		approved["const"] = true
	}
	return objectSchema(map[string]any{"approved": approved, "feedback": map[string]any{}}, "approved")
}

// ListWorkflowTemplates returns fresh, self-contained JSON Schemas for a small
// versioned catalog. No template code or provider protocol is executed.
func ListWorkflowTemplates() []WorkflowTemplate {
	text := map[string]any{"type": "string", "minLength": 1}
	step := objectSchema(map[string]any{
		"executor": map[string]any{"type": "string", "minLength": 1, "description": "Executor capability selected from get_workers; independent of the authoring client."},
		"task":     text, "params": map[string]any{"type": "object", "description": "Opaque parameters passed unchanged to the selected executor. Run inputs are supplied separately through bindings."},
		"output": objectSchema(map[string]any{"mime": text, "path": text, "schema": map[string]any{"type": []string{"object", "boolean"}}}),
	}, "executor")
	step["description"] = "An execute node with a result output port. Defaults to inline JSON; use output.schema to constrain it or output.path for an artifact."
	// Verification/review must produce inline JSON for the shared predicate.
	check := objectSchema(map[string]any{"executor": text, "task": text, "params": map[string]any{"type": "object"}}, "executor")
	check["description"] = "Receives request and result. Must emit inline JSON on result: {approved: boolean, feedback?: any}. Sequential verification requires approved:true."
	bound := map[string]any{"type": "integer", "minimum": 1, "maximum": 10000}
	input := objectSchema(map[string]any{"request": map[string]any{}}, "request")
	// The interpreter unwraps a subgraph with one output port.
	result := map[string]any{}
	var out []WorkflowTemplate
	add := func(name, description string, props map[string]any, in, output any, required ...string) {
		props["name"] = text
		out = append(out, WorkflowTemplate{Name: name, Version: "1", Description: description,
			ParameterSchema: schemaJSON(objectSchema(props, required...)), InputSchema: schemaJSON(in), OutputSchema: schemaJSON(output)})
	}
	add("sequential-verification", "Execute work then verify it. Verification result must be {approved:true}; false fails output validation.",
		map[string]any{"work": step, "verify": check}, input,
		objectSchema(map[string]any{"result": map[string]any{}, "verification": approvalSchema(true)}, "result", "verification"), "work", "verify")
	item := objectSchema(map[string]any{"id": text, "value": map[string]any{}}, "id", "value")
	add("bounded-parallel-map", "Transform items with unique string IDs under explicit concurrency and collection bounds. Results preserve input order.",
		map[string]any{"work": step, "concurrency": bound, "maxItems": bound},
		objectSchema(map[string]any{"items": map[string]any{"type": "array", "items": item}}, "items"),
		map[string]any{"type": "array", "items": objectSchema(map[string]any{"id": text, "result": map[string]any{}}, "id", "result")}, "work", "concurrency", "maxItems")
	add("review-repair-loop", "Repair then review, carrying the previous result and feedback. Stop on approval; fail on exhausted bound.",
		map[string]any{"repair": step, "review": check, "maxIterations": bound},
		objectSchema(map[string]any{"request": map[string]any{}, "initial": map[string]any{}}, "request", "initial"),
		objectSchema(map[string]any{"result": map[string]any{}, "review": approvalSchema(true)}, "result", "review"), "repair", "review", "maxIterations")
	add("external-approval", "Wait for a durable input response matching requestSchema, then pass it to work. Use a const approval predicate in requestSchema to require approval.",
		map[string]any{"work": step, "requestName": text, "requestSchema": map[string]any{"type": []string{"object", "boolean"}},
			"requestExpiresSeconds": map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 31536000},
			"requestPermissions":    map[string]any{"type": "array", "items": text, "uniqueItems": true}}, input, result, "work", "requestName", "requestSchema")
	add("command-pipeline", "Run a sequence of executor tasks with explicit data dependencies. Supply command adapter parameters in each step; no shell is added by the template.",
		map[string]any{"steps": map[string]any{"type": "array", "items": step, "minItems": 1, "maxItems": 100}}, input, result, "steps")
	return out
}

func GetWorkflowTemplate(name, version string) (WorkflowTemplate, error) {
	for _, t := range ListWorkflowTemplates() {
		if t.Name == name && t.Version == version {
			return t, nil
		}
	}
	return WorkflowTemplate{}, fmt.Errorf("unknown workflow template %q version %q; use list_workflow_templates", name, version)
}

func exprBinding(e *plan.Expr) plan.Binding { return plan.Binding{Expr: e} }
func refBinding(node, port string) plan.Binding {
	return plan.Binding{Ref: &plan.ValueRef{Node: node, Port: port}}
}
func fieldExpr(e *plan.Expr, name string) *plan.Expr {
	return &plan.Expr{Op: "field", Expr: e, Name: name}
}
func refExpr(node, port string) *plan.Expr { return &plan.Expr{Op: "ref", Node: node, Port: port} }
func literalExpr(value any) *plan.Expr     { return &plan.Expr{Op: "literal", Value: schemaJSON(value)} }

// InstantiateWorkflowTemplate compiles to ordinary Plan IR and validates both
// authoring parameters and the resulting plan. Inputs are never interpolated
// into tasks, argv, or expressions. There is no second execution engine.
func InstantiateWorkflowTemplate(name, version string, parameters json.RawMessage) (*plan.Plan, error) {
	t, err := GetWorkflowTemplate(name, version)
	if err != nil {
		return nil, err
	}
	if err := plan.ValidateInput(&plan.Plan{InputSchema: t.ParameterSchema}, parameters); err != nil {
		return nil, fmt.Errorf("template parameters: %w", err)
	}
	var p templateParameters
	if err := json.Unmarshal(parameters, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		p.Name = name
	}
	compiled := &plan.Plan{IRVersion: plan.CurrentIRVersion, Name: p.Name, Root: "root", InputSchema: t.InputSchema,
		OutputSchema: t.OutputSchema, Subgraphs: map[string]plan.Subgraph{}}
	executors := map[string]bool{}
	execute := func(id string, step templateStep, inputs map[string]plan.Binding) plan.Node {
		executors[step.Executor] = true
		output := step.Output
		if output.Mime == "" {
			output.Mime = "application/json"
		}
		if output.Path == "" && len(output.Schema) == 0 {
			output.Schema = schemaJSON(map[string]any{})
		}
		return plan.Node{ID: id, Kind: plan.KindExecute, Executor: step.Executor, Task: step.Task, Params: step.Params,
			Inputs: inputs, Outputs: map[string]plan.OutputDecl{"result": output}}
	}
	request := exprBinding(fieldExpr(&plan.Expr{Op: "input"}, "request"))
	var root plan.Subgraph
	switch name {
	case "sequential-verification":
		work := execute("work", p.Work, map[string]plan.Binding{"request": request})
		verify := execute("verify", p.Verify, map[string]plan.Binding{"request": request, "result": refBinding("work", "result")})
		verify.Outputs["result"] = plan.OutputDecl{Mime: "application/json", Schema: schemaJSON(approvalSchema(true))}
		root = plan.Subgraph{Nodes: []plan.Node{work, verify}, Output: map[string]plan.Binding{"result": refBinding("work", "result"), "verification": refBinding("verify", "result")}}
	case "bounded-parallel-map":
		root = plan.Subgraph{Nodes: []plan.Node{{ID: "map", Kind: plan.KindMap, Items: fieldExpr(&plan.Expr{Op: "input"}, "items"),
			Key: fieldExpr(&plan.Expr{Op: "item"}, "id"), Concurrency: &p.Concurrency, MaxItems: &p.MaxItems, BodyRef: "map_body"}},
			Output: map[string]plan.Binding{"results": refBinding("map", "items")}}
		compiled.Subgraphs["map_body"] = plan.Subgraph{Nodes: []plan.Node{execute("work", p.Work, map[string]plan.Binding{"item": exprBinding(&plan.Expr{Op: "item"})})},
			Output: map[string]plan.Binding{"id": exprBinding(fieldExpr(&plan.Expr{Op: "item"}, "id")), "result": refBinding("work", "result")}}
		var in map[string]any
		_ = json.Unmarshal(compiled.InputSchema, &in)
		in["properties"].(map[string]any)["items"].(map[string]any)["maxItems"] = p.MaxItems
		compiled.InputSchema = schemaJSON(in)
	case "review-repair-loop":
		repair := execute("repair", p.Repair, map[string]plan.Binding{"request": request, "previous": exprBinding(&plan.Expr{Op: "state"})})
		review := execute("review", p.Review, map[string]plan.Binding{"request": request, "result": refBinding("repair", "result")})
		review.Outputs["result"] = plan.OutputDecl{Mime: "application/json", Schema: schemaJSON(approvalSchema(false))}
		compiled.Subgraphs["loop_body"] = plan.Subgraph{Nodes: []plan.Node{repair, review}, Output: map[string]plan.Binding{"result": refBinding("repair", "result"), "review": refBinding("review", "result")}}
		root = plan.Subgraph{Nodes: []plan.Node{{ID: "loop", Kind: plan.KindLoop, BodyRef: "loop_body", MaxIterations: &p.MaxIterations, OnExhausted: "fail",
			InitState: &plan.Expr{Op: "object", Fields: map[string]*plan.Expr{"result": fieldExpr(&plan.Expr{Op: "input"}, "initial"), "review": literalExpr(map[string]any{"approved": false})}},
			While:     &plan.Expr{Op: "not", Expr: fieldExpr(fieldExpr(&plan.Expr{Op: "state"}, "review"), "approved")}}},
			Output: map[string]plan.Binding{"result": exprBinding(fieldExpr(refExpr("loop", "result"), "result")), "review": exprBinding(fieldExpr(refExpr("loop", "result"), "review"))}}
	case "external-approval":
		root = plan.Subgraph{Nodes: []plan.Node{{ID: "approval", Kind: plan.KindWait, WaitFor: "input", SignalName: p.RequestName, RequestSchema: p.RequestSchema,
			RequestExpiresSeconds: p.RequestExpiresSeconds, RequestPermissions: p.RequestPermissions, Inputs: map[string]plan.Binding{"request": request}},
			execute("work", p.Work, map[string]plan.Binding{"request": request, "approval": refBinding("approval", "response")})},
			Output: map[string]plan.Binding{"result": refBinding("work", "result")}}
	case "command-pipeline":
		root.Nodes = []plan.Node{}
		for i, step := range p.Steps {
			inputs := map[string]plan.Binding{"request": request}
			if i > 0 {
				inputs["previous"] = refBinding(fmt.Sprintf("step_%d", i-1), "result")
			}
			root.Nodes = append(root.Nodes, execute(fmt.Sprintf("step_%d", i), step, inputs))
		}
		root.Output = map[string]plan.Binding{"result": refBinding(root.Nodes[len(root.Nodes)-1].ID, "result")}
	}
	compiled.Subgraphs["root"] = root
	for executor := range executors {
		compiled.RequiredExecutors = append(compiled.RequiredExecutors, executor)
	}
	sort.Strings(compiled.RequiredExecutors)
	for _, issue := range plan.Validate(compiled) {
		if issue.Severity != "warning" {
			return nil, fmt.Errorf("compiled template: %s: %s", issue.Code, issue.Message)
		}
	}
	for _, subgraph := range compiled.Subgraphs {
		for _, node := range subgraph.Nodes {
			for _, output := range node.Outputs {
				if failure, ok := plan.ValidateInputResponse(output.Schema, nil).(*plan.ValueValidationFailure); ok && failure.Code == plan.InvalidSchema {
					return nil, fmt.Errorf("template output schema: %w", failure)
				}
			}
		}
	}
	if _, err := compiled.ComputeDigest(); err != nil {
		return nil, err
	}
	return compiled, nil
}
