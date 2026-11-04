package mcp

import (
	"encoding/json"

	"github.com/muxlio/muxlio-core/internal/plan"
)

func featureInputSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"features": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 20,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{
						"id":      map[string]any{"type": "string", "minLength": 1, "description": "Unique feature ID within this submission."},
						"feature": map[string]any{"type": "string", "minLength": 1, "description": "The requested feature."},
						"context": map[string]any{"type": "string", "description": "Relevant details and constraints."},
					}, "required": []string{"id", "feature"},
				},
			},
			"branch": map[string]any{"type": "string", "description": "Optional branch context for the implementation agent; does not check out or create a branch."},
		}, "required": []string{"features"},
	}
}

// newFeaturePlan constructs ordinary Plan IR; it neither reads recipe files nor
// invokes a compiler. Each call owns its nodes, schemas, and content digest.
func newFeaturePlan() (*plan.Plan, error) {
	item := &plan.Expr{Op: "item"}
	state := &plan.Expr{Op: "state"}
	field := func(expr *plan.Expr, name string) *plan.Expr { return &plan.Expr{Op: "field", Expr: expr, Name: name} }
	ref := func(node, port string) *plan.Expr { return &plan.Expr{Op: "ref", Node: node, Port: port} }
	literal := func(value any) *plan.Expr { return &plan.Expr{Op: "literal", Value: featureJSON(value)} }
	bind := func(expr *plan.Expr) plan.Binding { return plan.Binding{Expr: expr} }
	not := func(expr *plan.Expr) *plan.Expr { return &plan.Expr{Op: "not", Expr: expr} }
	nextAttempt := &plan.Expr{Op: "add", Left: field(state, "attempts"), Right: literal(1)}
	feature := func(expr *plan.Expr) map[string]plan.Binding { return map[string]plan.Binding{"feature": bind(expr)} }

	clean := featureCommand("require-clean-workspace", "Require a clean workspace before beginning a feature.", "git", []string{"status", "--porcelain"})
	clean.Params = featureJSON(map[string]any{"program": "git", "args": []string{"status", "--porcelain"}, "cwd": ".", "onNonZero": "fail", "failOnOutput": true})
	clean.Inputs = feature(item)
	implement := featureAgent("implement", `Implement the requested feature in the current workspace, following existing conventions and patterns. Add or update focused tests where relevant and run applicable checks. Write notes.md with changes, exact commands, and exit codes. Work only on this feature. Do not delegate, commit, or push; the workflow creates local commits.`, "notes", "notes.md")
	implement.Inputs = feature(item)
	implement.Inputs["branch"] = bind(field(&plan.Expr{Op: "input"}, "branch"))
	implement.Inputs["__after_clean"] = bind(ref(clean.ID, "passed"))
	review := featureAgent("review", `Independently review the feature implementation and the supplied implementation notes for correctness, regressions, security, and test coverage. Fix issues directly in the workspace. Write review.md with findings, fixes, and verification; if nothing needed fixing, state what was inspected. Do not delegate, edit SKILL.md, commit, or push.`, "report", "review.md")
	review.Inputs = feature(item)
	review.Inputs["notes"] = bind(ref(implement.ID, "notes"))

	verify := featureCommand("verify", "Run repository build and test suites; return nonzero if any suite fails.", "node", []string{"-e", featureVerificationScript})
	verify.Params = featureJSON(map[string]any{"program": "node", "args": []string{"-e", featureVerificationScript}, "cwd": ".", "onNonZero": "return", "timeout": "10m"})
	verify.Inputs = feature(field(state, "feature"))
	fix := featureAgent("fix", `Read the supplied verification failure report, find the root causes, and fix them without weakening or disabling checks. Keep changes scoped to the feature. Write fix.md describing the failure and repairs. Do not delegate, edit SKILL.md, commit, or push.`, "notes", "fix.md")
	fix.Inputs = feature(field(state, "feature"))
	failures := map[string]*plan.Expr{}
	for port := range verify.Outputs {
		failures[port] = ref(verify.ID, port)
	}
	fix.Inputs["failures"] = bind(&plan.Expr{Op: "object", Fields: failures})

	stage := featureCommand("stage-changes", "Stage workspace changes for the verified feature commit.", "git", []string{"add", "-A"})
	stage.Inputs = feature(item)
	commit := featureCommand("commit", "Create the verified local feature commit.", "git", []string{"commit", "-m", ""})
	commit.Params = featureJSON(map[string]any{"program": "git", "args": []string{"commit", "-m", ""}, "argTemplates": []any{nil, nil, "implement {{feature.feature}}"}, "cwd": ".", "onNonZero": "fail"})
	commit.Inputs = feature(item)
	commit.Inputs["__after_stage"] = bind(ref(stage.ID, "passed"))

	one, twenty, ten := 1, 20, 10
	p := &plan.Plan{
		IRVersion: plan.CurrentIRVersion, Name: "implement-features", Root: "root",
		InputSchema: featureJSON(featureInputSchema()), RequiredExecutors: []string{"agent/codex", "agent/kiro", "command"},
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes:  []plan.Node{{ID: "features", Kind: plan.KindMap, Items: field(&plan.Expr{Op: "input"}, "features"), Key: field(item, "id"), Concurrency: &one, MaxItems: &twenty, BodyRef: "feature"}},
				Output: map[string]plan.Binding{"results": bind(ref("features", "items"))},
			},
			"feature": {
				Nodes: []plan.Node{clean, implement, review, {
					ID: "verification", Kind: plan.KindLoop, BodyRef: "verification-attempt", MaxIterations: &ten, OnExhausted: "pause",
					While: not(field(state, "passed")),
					// Loops have their own item context; carry the map item explicitly.
					InitState: &plan.Expr{Op: "object", Fields: map[string]*plan.Expr{"feature": item, "passed": literal(false), "attempts": literal(0)}},
					Inputs:    map[string]plan.Binding{"__after_review": bind(ref(review.ID, "report"))},
				}, {
					ID: "commit-if-verified", Kind: plan.KindBranch, Condition: field(ref("verification", "result"), "passed"), ThenRef: "commit-feature", ElseRef: "noop",
				}},
				Output: map[string]plan.Binding{"id": bind(field(item, "id")), "attempts": bind(field(ref("verification", "result"), "attempts"))},
			},
			"verification-attempt": {
				Nodes: []plan.Node{verify, {
					ID: "repair-if-needed", Kind: plan.KindBranch, ThenRef: "repair-feature", ElseRef: "noop",
					Condition: &plan.Expr{Op: "and", Left: not(ref(verify.ID, "passed")), Right: &plan.Expr{Op: "lt", Left: nextAttempt, Right: literal(10)}},
				}, {
					ID: "next-state", Kind: plan.KindProject,
					Inputs: map[string]plan.Binding{"passed": bind(ref(verify.ID, "passed")), "attempts": bind(nextAttempt), "feature": bind(field(state, "feature")), "__after_repair": bind(ref("repair-if-needed", "result"))},
				}},
				Output: map[string]plan.Binding{"passed": bind(ref("next-state", "passed")), "attempts": bind(ref("next-state", "attempts")), "feature": bind(ref("next-state", "feature"))},
			},
			"repair-feature": {Nodes: []plan.Node{fix}, Output: map[string]plan.Binding{"result": bind(literal(nil))}},
			"commit-feature": {Nodes: []plan.Node{stage, commit}},
			"noop":           {Nodes: []plan.Node{}, Output: map[string]plan.Binding{"result": bind(literal(nil))}},
		},
	}
	_, err := p.ComputeDigest()
	return p, err
}

func featureAgent(id, task, output, path string) plan.Node {
	return plan.Node{ID: id, Kind: plan.KindExecute, Executor: "agent/kiro", FallbackExecutors: []string{"agent/codex"}, Task: task,
		Outputs: map[string]plan.OutputDecl{output: {Mime: "text/markdown", Path: path}}, Retry: &plan.RetryPolicy{MaxAttempts: 3, Effect: "retryable"}}
}

func featureCommand(id, task, program string, args []string) plan.Node {
	outputs := map[string]plan.OutputDecl{}
	for name, schema := range map[string]any{
		"passed": map[string]any{"type": "boolean"}, "stdout": map[string]any{"type": "string"}, "stderr": map[string]any{"type": "string"}, "durationMs": map[string]any{"type": "number"},
		"exitCode": map[string]any{"anyOf": []any{map[string]any{"type": "number"}, map[string]any{"type": "null"}}},
		"signal":   map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
	} {
		outputs[name] = plan.OutputDecl{Mime: "application/json", Schema: featureJSON(schema)}
	}
	return plan.Node{ID: id, Kind: plan.KindExecute, Executor: "command", Task: task, Outputs: outputs,
		Params: featureJSON(map[string]any{"program": program, "args": args, "cwd": ".", "onNonZero": "fail"})}
}

// All callers supply Go literals and schema values, never arbitrary user data.
func featureJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

const featureVerificationScript = `const {spawnSync}=require('node:child_process');
const checks=[['npm run build','npm',['run','build'],'.'],['npm test','npm',['test'],'.'],['UI tests','npm',['--prefix','muxlio-ui','test'],'.'],['core Go tests','go',['test','./...'],'muxlio-core'],['adapter Go tests','go',['test','./...'],'muxlio-adapters']];
let failed=false;
for(const [label,program,args,cwd] of checks){process.stdout.write('\n=== '+label+' ===\n');const r=spawnSync(program,args,{cwd,stdio:'inherit'});const code=r.error?1:(r.status??1);process.stdout.write('\n=== '+label+': exit '+code+' ===\n');if(code!==0)failed=true;}
process.exitCode=failed?1:0;`
