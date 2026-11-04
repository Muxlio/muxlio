package mcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type featurePlanExecutor struct {
	verifyResults []bool
	verifyCalls   int
	failAt        string
	calls         []string
	fixInputs     []map[string]any
}

func (f *featurePlanExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	feature, ok := req.Inputs["feature"].(map[string]any)
	if !ok || feature["id"] == nil {
		return workers.ExecuteResult{}, fmt.Errorf("%s lost feature context", req.NodeID)
	}
	f.calls = append(f.calls, fmt.Sprint(feature["id"])+":"+req.NodeID)
	if req.NodeID == f.failAt {
		return workers.ExecuteResult{Error: "fixture failure"}, nil
	}
	if req.NodeID == "fix" {
		f.fixInputs = append(f.fixInputs, req.Inputs)
	}
	passed := true
	if req.NodeID == "verify" {
		passed = f.verifyCalls < len(f.verifyResults) && f.verifyResults[f.verifyCalls]
		f.verifyCalls++
	}
	code := 0
	if !passed {
		code = 1
	}
	return workers.ExecuteResult{Outputs: map[string]any{
		"passed": passed, "exitCode": code, "signal": nil, "stdout": "verification output", "stderr": "verification details", "durationMs": 1,
		"notes":  map[string]any{"digest": "sha256:fixture", "mime": "text/markdown"},
		"report": map[string]any{"digest": "sha256:fixture", "mime": "text/markdown"},
	}}, nil
}

func TestConstructedFeaturePlanExecution(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		results                  []bool
		failAt                   string
		wantState                string
		verifies, fixes, commits int
	}{
		{"immediate success", []bool{true, true}, "", plan.RunSucceeded, 2, 0, 2},
		{"fix then success", []bool{false, true, true}, "", plan.RunSucceeded, 3, 1, 2},
		{"last attempt success", append(make([]bool, 9), true, true), "", plan.RunSucceeded, 11, 9, 2},
		{"exhaustion pauses before commit", make([]bool, 10), "", plan.RunPaused, 10, 9, 0},
		{"dirty workspace stops implementation", nil, "require-clean-workspace", plan.RunFailed, 0, 0, 0},
		{"review failure stops verification", nil, "review", plan.RunFailed, 0, 0, 0},
		{"stage failure prevents commit", []bool{true}, "stage-changes", plan.RunFailed, 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := newFeaturePlan()
			if err != nil {
				t.Fatal(err)
			}
			if errs := plan.Validate(p); len(errs) > 0 {
				t.Fatalf("invalid constructed plan: %v", errs)
			}
			if err := p.VerifyDigest(); err != nil {
				t.Fatal(err)
			}
			fixture := &featurePlanExecutor{verifyResults: tc.results, failAt: tc.failAt}
			engine := plan.NewEngine(p, fixture)
			id, err := engine.CreateRun(json.RawMessage(`{"features":[{"id":"one","feature":"First feature","context":"Keep it scoped"},{"id":"two","feature":"Second feature"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			err = engine.Run(id)
			if tc.wantState != plan.RunFailed && err != nil {
				t.Fatal(err)
			}
			if tc.wantState == plan.RunFailed && err == nil {
				t.Fatal("expected failed execution")
			}
			run, err := engine.Store().GetRun(id)
			if err != nil || run.State != tc.wantState {
				t.Fatalf("run=%+v error=%v", run, err)
			}
			fixes, commits := 0, 0
			firstCommit, secondStart := -1, -1
			for i, call := range fixture.calls {
				if strings.HasSuffix(call, ":fix") {
					fixes++
				}
				if strings.HasSuffix(call, ":commit") {
					commits++
					if firstCommit < 0 {
						firstCommit = i
					}
				}
				if strings.HasPrefix(call, "two:") && secondStart < 0 {
					secondStart = i
				}
			}
			if fixture.verifyCalls != tc.verifies || fixes != tc.fixes || commits != tc.commits {
				t.Fatalf("wrong execution counts: %v", fixture.calls)
			}
			if tc.wantState == plan.RunSucceeded {
				if firstCommit < 0 || secondStart <= firstCommit {
					t.Fatalf("features were not committed serially: %v", fixture.calls)
				}
				results, ok := run.Output.([]any)
				if !ok || len(results) != 2 {
					t.Fatalf("unexpected output: %#v", run.Output)
				}
			} else if secondStart >= 0 {
				t.Fatalf("started another feature before finishing the first: %v", fixture.calls)
			}
			for _, input := range fixture.fixInputs {
				failures := input["failures"].(map[string]any)
				if failures["passed"] != false || failures["stdout"] != "verification output" || failures["stderr"] != "verification details" {
					t.Fatalf("fix lost failure details: %#v", input)
				}
			}
			if tc.failAt == "require-clean-workspace" && !reflect.DeepEqual(fixture.calls, []string{"one:require-clean-workspace"}) {
				t.Fatalf("dirty workspace was modified: %v", fixture.calls)
			}
		})
	}
}

func TestConstructedFeaturePlanIsFreshAndDeterministic(t *testing.T) {
	first, err := newFeaturePlan()
	if err != nil {
		t.Fatal(err)
	}
	digest := first.Digest
	first.Subgraphs["feature"].Nodes[0].Task = "modified by caller"
	second, err := newFeaturePlan()
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest != digest || second.Subgraphs["feature"].Nodes[0].Task == "modified by caller" {
		t.Fatal("plan construction shares mutable state or has unstable digests")
	}
	for _, graph := range second.Subgraphs {
		for _, node := range graph.Nodes {
			if node.Executor != "command" {
				continue
			}
			var params struct {
				Program string   `json:"program"`
				Args    []string `json:"args"`
			}
			if err := json.Unmarshal(node.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Program == "git" && len(params.Args) > 0 && params.Args[0] == "push" {
				t.Fatal("constructed plan must never push")
			}
		}
	}
}
