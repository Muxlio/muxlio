package plan_test

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

//go:embed testdata/repeat_until_10.plan.json
var repeatUntil10PlanJSON []byte

type countingLoopExecutor struct {
	verifyResults []bool
	verifyCalls   int
	fixCalls      int
}

func (e *countingLoopExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	switch req.NodeID {
	case "verify":
		idx := e.verifyCalls
		e.verifyCalls++
		passed := false
		if idx < len(e.verifyResults) {
			passed = e.verifyResults[idx]
		}
		return workers.ExecuteResult{Outputs: map[string]any{
			"passed": passed,
			"report": map[string]any{"mime": "text/markdown", "path": "verification.md"},
		}}, nil
	case "fix":
		e.fixCalls++
		return workers.ExecuteResult{Outputs: map[string]any{
			"notes": map[string]any{"mime": "text/markdown", "path": "fix.md"},
		}}, nil
	default:
		return workers.ExecuteResult{}, nil
	}
}

func loadRepeatUntilPlan(t *testing.T) *plan.Plan {
	t.Helper()
	var p plan.Plan
	if err := json.Unmarshal(repeatUntil10PlanJSON, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestRepeatUntilExhaustionRunsTenVerifiesAndNineFixes(t *testing.T) {
	p := loadRepeatUntilPlan(t)
	exec := &countingLoopExecutor{verifyResults: make([]bool, 20)}
	engine := plan.NewEngine(p, exec)
	runID, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	run, _ := engine.Store().GetRun(runID)
	if run.State != plan.RunPaused {
		t.Fatalf("expected paused, got %q", run.State)
	}
	if exec.verifyCalls != 10 {
		t.Fatalf("verify calls = %d, want 10", exec.verifyCalls)
	}
	if exec.fixCalls != 9 {
		t.Fatalf("fix calls = %d, want 9", exec.fixCalls)
	}
}

func TestRepeatUntilEventualSuccessThreeVerifiesTwoFixes(t *testing.T) {
	p := loadRepeatUntilPlan(t)
	exec := &countingLoopExecutor{verifyResults: []bool{false, false, true}}
	engine := plan.NewEngine(p, exec)
	runID, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	run, _ := engine.Store().GetRun(runID)
	if run.State != plan.RunSucceeded {
		t.Fatalf("expected succeeded, got %q", run.State)
	}
	if exec.verifyCalls != 3 {
		t.Fatalf("verify calls = %d, want 3", exec.verifyCalls)
	}
	if exec.fixCalls != 2 {
		t.Fatalf("fix calls = %d, want 2", exec.fixCalls)
	}
}

func TestRepeatUntilFixInputReferencesCaptureReport(t *testing.T) {
	p := loadRepeatUntilPlan(t)
	loop := p.Subgraphs["root"].Nodes[0]
	body := p.Subgraphs[loop.BodyRef]
	fixSGName := ""
	for _, n := range body.Nodes {
		if n.Kind == plan.KindBranch {
			fixSGName = n.ThenRef
			break
		}
	}
	if fixSGName == "" {
		t.Fatal("missing fix branch")
	}
	fixSG := p.Subgraphs[fixSGName]
	var fixNode *plan.Node
	for i := range fixSG.Nodes {
		if fixSG.Nodes[i].ID == "fix" {
			fixNode = &fixSG.Nodes[i]
			break
		}
	}
	if fixNode == nil {
		t.Fatal("missing fix node")
	}
	b := fixNode.Inputs["failures"]
	if b.Ref == nil || b.Ref.Node != "loop__cap" || b.Ref.Port != "value_output" {
		t.Fatalf("fix failures binding = %#v, want ref loop__cap.value_output", b)
	}
}

func TestRepeatUntilBranchConditionRequiresRemainingAttempts(t *testing.T) {
	p := loadRepeatUntilPlan(t)
	loop := p.Subgraphs["root"].Nodes[0]
	body := p.Subgraphs[loop.BodyRef]
	var branch *plan.Node
	for i := range body.Nodes {
		if body.Nodes[i].Kind == plan.KindBranch {
			branch = &body.Nodes[i]
			break
		}
	}
	if branch == nil || branch.Condition == nil {
		t.Fatal("missing branch")
	}
	if branch.Condition.Op != "and" {
		t.Fatalf("branch condition op = %q, want and", branch.Condition.Op)
	}
}

func activationCount(store state.Store, runID, nodeID string) int {
	acts, err := store.ListActivations(runID)
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range acts {
		if a.NodeID == nodeID && a.State == plan.ActSucceeded {
			n++
		}
	}
	return n
}

func TestRepeatUntilImmediateSuccessOneVerifyZeroFixes(t *testing.T) {
	p := loadRepeatUntilPlan(t)
	exec := &countingLoopExecutor{verifyResults: []bool{true}}
	engine := plan.NewEngine(p, exec)
	runID, _ := engine.CreateRun(json.RawMessage(`{}`))
	if err := engine.Run(runID); err != nil {
		t.Fatal(err)
	}
	if exec.verifyCalls != 1 || exec.fixCalls != 0 {
		t.Fatalf("verify=%d fix=%d, want 1/0", exec.verifyCalls, exec.fixCalls)
	}
	_ = activationCount(engine.Store(), runID, "verify")
}
