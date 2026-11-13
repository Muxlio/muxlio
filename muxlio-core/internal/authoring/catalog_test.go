package authoring

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/workers"
)

var catalogParameters = map[string]string{
	"sequential-verification": `{"work":{"executor":"generic/work","task":"Summarize the supplied report."},"verify":{"executor":"generic/check","task":"Check factual accuracy."}}`,
	"bounded-parallel-map":    `{"work":{"executor":"generic/work"},"concurrency":2,"maxItems":3}`,
	"review-repair-loop":      `{"repair":{"executor":"generic/work"},"review":{"executor":"generic/check"},"maxIterations":2}`,
	"external-approval":       `{"work":{"executor":"generic/work"},"requestName":"publish","requestSchema":{"const":true},"requestPermissions":["approve"],"requestExpiresSeconds":60}`,
	"command-pipeline":        `{"steps":[{"executor":"generic/first","params":{"program":"transform","args":["a b"]}},{"executor":"generic/second"}]}`,
}

func instantiate(t *testing.T, name string) *plan.Plan {
	t.Helper()
	p, err := InstantiateWorkflowTemplate(name, "1", json.RawMessage(catalogParameters[name]))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCatalogSchemasPlansAndDigests(t *testing.T) {
	t.Setenv("PATH", "")
	list := ListWorkflowTemplates()
	if len(list) != 5 {
		t.Fatalf("templates: %d", len(list))
	}
	for _, template := range list {
		t.Run(template.Name, func(t *testing.T) {
			p := instantiate(t, template.Name)
			if p.Digest == "" || p.VerifyDigest() != nil {
				t.Fatal("missing or invalid digest")
			}
			if issues := plan.ErrorsOnly(plan.Validate(p)); len(issues) != 0 {
				t.Fatal(issues)
			}
			other := instantiate(t, template.Name)
			if p.Digest != other.Digest {
				t.Fatal("nondeterministic instantiation")
			}
			p.Name += "-edited"
			if p.VerifyDigest() == nil {
				t.Fatal("editing should invalidate old digest")
			}
			p.Digest = ""
			if _, err := p.ComputeDigest(); err != nil || p.Digest == other.Digest {
				t.Fatal("edited ordinary plan did not rehash", err)
			}
		})
	}
	if _, err := GetWorkflowTemplate("../task", "1"); err == nil {
		t.Fatal("accepted unknown name")
	}
	if _, err := GetWorkflowTemplate("command-pipeline", "2"); err == nil {
		t.Fatal("accepted unknown version")
	}
	if _, err := GetWorkflowTemplate("command-pipeline", ""); err == nil {
		t.Fatal("accepted unpinned template version")
	}
}

func TestCatalogRejectsInvalidParametersAndInputs(t *testing.T) {
	for _, tc := range []struct{ name, parameters string }{
		{"bounded-parallel-map", `{"work":{"executor":"x"},"concurrency":0,"maxItems":2}`},
		{"bounded-parallel-map", `{"work":{"executor":"x"},"concurrency":1.5,"maxItems":2}`},
		{"review-repair-loop", `{"repair":{"executor":"x"},"review":{"executor":"y"},"maxIterations":0}`},
		{"sequential-verification", `{"work":{"executor":"x"},"verify":{"executor":"y","output":{"path":"unchecked.json"}}}`},
		{"command-pipeline", `{"steps":[]}`},
		{"command-pipeline", `{"steps":[{"executor":""}]}`},
		{"command-pipeline", `{"steps":[{"executor":"x","unknown":1}]}`},
		{"command-pipeline", `{"steps":[{"executor":"x","output":{"schema":{"type":"invalid"}}}]}`},
		{"external-approval", `{"work":{"executor":"x"},"requestName":"approve","requestSchema":{"type":"invalid"}}`},
		{"external-approval", `{"work":{"executor":"x"},"requestName":"approve","requestSchema":{"$ref":"https://example.invalid/schema"}}`},
		{"command-pipeline", `null`},
		{"command-pipeline", `{} {}`},
	} {
		if _, err := InstantiateWorkflowTemplate(tc.name, "1", json.RawMessage(tc.parameters)); err == nil {
			t.Errorf("accepted %s %s", tc.name, tc.parameters)
		}
	}
	p := instantiate(t, "bounded-parallel-map")
	for _, input := range []string{`{}`, `{"items":[{"id":"a","value":1},{"id":"a","value":2}]}`, `{"items":[{"id":"a","value":1},{"id":"b","value":2},{"id":"c","value":3},{"id":"d","value":4}]}`} {
		if err := plan.ValidateInput(p, json.RawMessage(input)); err == nil {
			t.Errorf("accepted input %s", input)
		}
	}
}

type catalogExecutor struct {
	mu         sync.Mutex
	requests   []workers.ExecuteRequest
	checkCalls int
	approveAt  int
}

func (e *catalogExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, req)
	var result any = req.Inputs
	if req.NodeID == "verify" || req.NodeID == "review" {
		e.checkCalls++
		result = map[string]any{"approved": e.approveAt > 0 && e.checkCalls >= e.approveAt, "feedback": "Check the source date."}
	}
	return workers.ExecuteResult{Outputs: map[string]any{"result": result}}, nil
}

func runCatalog(t *testing.T, p *plan.Plan, input string, executor workers.Executor) *plan.Engine {
	t.Helper()
	e := plan.NewEngine(p, executor)
	id, err := e.CreateRun(json.RawMessage(input))
	if err != nil {
		t.Fatal(err)
	}
	_ = e.Run(id) // Failed execute/loop runs are inspected through persisted state.
	return e
}

func assertCatalogRun(t *testing.T, e *plan.Engine, want string) {
	t.Helper()
	runs, err := e.Store().ListRuns()
	if err != nil || len(runs) != 1 || runs[0].State != want {
		t.Fatalf("want %s: %+v %v", want, runs, err)
	}
	if want == plan.RunSucceeded {
		if err := plan.ValidateInput(&plan.Plan{InputSchema: e.Plan().OutputSchema}, schemaJSON(runs[0].Output)); err != nil {
			t.Fatalf("output schema mismatch: %v", err)
		}
	}
}

func TestSequentialVerificationGatesSuccess(t *testing.T) {
	for _, approveAt := range []int{0, 1} {
		exec := &catalogExecutor{approveAt: approveAt}
		e := runCatalog(t, instantiate(t, "sequential-verification"), `{"request":"A museum exhibition summary"}`, exec)
		want := plan.RunFailed
		if approveAt == 1 {
			want = plan.RunSucceeded
		}
		assertCatalogRun(t, e, want)
		if len(exec.requests) != 2 || exec.requests[1].Inputs["result"] == nil {
			t.Fatal("verification did not consume work result")
		}
	}
}

func TestCatalogCheckSchemasRejectMalformedApproval(t *testing.T) {
	for _, name := range []string{"sequential-verification", "review-repair-loop"} {
		p := instantiate(t, name)
		for _, sg := range p.Subgraphs {
			for _, node := range sg.Nodes {
				if node.ID != "verify" && node.ID != "review" {
					continue
				}
				for _, outputs := range []map[string]any{
					{}, {"result": nil}, {"result": "approved"},
					{"result": map[string]any{}}, {"result": map[string]any{"approved": "true"}},
				} {
					if err := plan.ValidateExecuteOutputs(node.Outputs, outputs); err == nil {
						t.Errorf("%s accepted malformed approval: %#v", name, outputs)
					}
				}
			}
		}
	}
}

func TestCatalogReturnsIndependentMetadataAndPlans(t *testing.T) {
	first := ListWorkflowTemplates()
	first[0].ParameterSchema[0] = '!'
	if !json.Valid(ListWorkflowTemplates()[0].ParameterSchema) {
		t.Fatal("caller mutated shared catalog metadata")
	}
	p := instantiate(t, "command-pipeline")
	original := p.Digest
	p.Subgraphs["root"].Nodes[0].Executor = "generic/other"
	if p.VerifyDigest() == nil {
		t.Fatal("executor change did not invalidate digest")
	}
	if other := instantiate(t, "command-pipeline"); other.Digest != original {
		t.Fatal("caller mutated shared compiled plan")
	}
}

type boundedCatalogExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *boundedCatalogExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	e.started <- struct{}{}
	<-e.release
	return workers.ExecuteResult{Outputs: map[string]any{"result": req.Inputs["item"]}}, nil
}

func TestCatalogMapBoundsDispatchAndAcceptsEmptyInput(t *testing.T) {
	p := instantiate(t, "bounded-parallel-map") // concurrency=2, maxItems=3
	empty := &catalogExecutor{}
	e := runCatalog(t, p, `{"items":[]}`, empty)
	assertCatalogRun(t, e, plan.RunSucceeded)
	if len(empty.requests) != 0 {
		t.Fatal("empty map dispatched work")
	}

	exec := &boundedCatalogExecutor{started: make(chan struct{}, 3), release: make(chan struct{}, 3)}
	e = plan.NewEngine(p, exec)
	id, err := e.CreateRun(json.RawMessage(`{"items":[{"id":"a","value":1},{"id":"b","value":2},{"id":"c","value":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(id) }()
	// Always unblock dispatch on failure so the test cannot leak a blocked run.
	defer close(exec.release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		select {
		case <-exec.started:
		case <-ctx.Done():
			t.Fatal("map did not dispatch up to its concurrency bound")
		}
	}
	select {
	case <-exec.started:
		t.Fatal("map dispatched beyond its concurrency bound")
	default:
	}
	exec.release <- struct{}{}
	select {
	case <-exec.started:
	case <-ctx.Done():
		t.Fatal("map did not dispatch remaining item after a slot was released")
	}
	exec.release <- struct{}{}
	exec.release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("bounded map did not complete")
	}
	assertCatalogRun(t, e, plan.RunSucceeded)
}

func TestReviewRepairCarriesFeedbackAndHonorsBound(t *testing.T) {
	for _, approveAt := range []int{0, 2} {
		exec := &catalogExecutor{approveAt: approveAt}
		e := runCatalog(t, instantiate(t, "review-repair-loop"), `{"request":"Improve the visitor guide","initial":"Draft"}`, exec)
		want := plan.RunFailed
		if approveAt == 2 {
			want = plan.RunSucceeded
		}
		assertCatalogRun(t, e, want)
		if exec.checkCalls != 2 || len(exec.requests) != 4 {
			t.Fatalf("unbounded or incomplete loop: %+v", exec.requests)
		}
		previous := exec.requests[2].Inputs["previous"].(map[string]any)
		if previous["review"].(map[string]any)["feedback"] != "Check the source date." {
			t.Fatal("feedback was not carried")
		}
	}
}

func TestMapAndPipelineUseOrdinaryDependencies(t *testing.T) {
	exec := &catalogExecutor{}
	e := runCatalog(t, instantiate(t, "bounded-parallel-map"), `{"items":[{"id":"a","value":"Painting"},{"id":"b","value":"Sculpture"}]}`, exec)
	assertCatalogRun(t, e, plan.RunSucceeded)
	runs, _ := e.Store().ListRuns()
	results := runs[0].Output.([]any)
	if results[0].(map[string]any)["id"] != "a" || results[1].(map[string]any)["id"] != "b" {
		t.Fatal("map reordered input")
	}
	exec = &catalogExecutor{}
	e = runCatalog(t, instantiate(t, "command-pipeline"), `{"request":"Convert the survey data"}`, exec)
	assertCatalogRun(t, e, plan.RunSucceeded)
	if len(exec.requests) != 2 || !reflect.DeepEqual(exec.requests[1].Inputs["previous"], exec.requests[0].Inputs) {
		t.Fatal("pipeline did not consume previous output")
	}
	var params map[string]any
	_ = json.Unmarshal(exec.requests[0].Params, &params)
	if !reflect.DeepEqual(params["args"], []any{"a b"}) {
		t.Fatal("argv changed")
	}
}

func TestExternalApprovalWaitsBeforeDispatch(t *testing.T) {
	p := instantiate(t, "external-approval")
	exec := &catalogExecutor{}
	e := plan.NewEngine(p, exec)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e.Context = ctx
	id, err := e.CreateRun(json.RawMessage(`{"request":"Publish the gallery guide"}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run(id) }()
	for {
		requests, err := e.Store().ListInputRequests(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(requests) == 1 {
			req := requests[0]
			exec.mu.Lock()
			calls := len(exec.requests)
			exec.mu.Unlock()
			if calls != 0 || req.Name != "publish" || req.ExpiresAt == nil || !reflect.DeepEqual(req.Permissions, []string{"approve"}) {
				t.Fatalf("approval did not gate dispatch: %+v", req)
			}
			if err := plan.ValidateInputResponse(req.Schema, false); err == nil {
				t.Fatal("denial satisfied approval schema")
			}
			if _, _, err := e.Store().SubmitInputResponse(id, req.RequestID, true, "reviewer", "approval-1"); err != nil {
				t.Fatal(err)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("input request did not appear")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("approval did not resume")
	}
	assertCatalogRun(t, e, plan.RunSucceeded)
	if len(exec.requests) != 1 || exec.requests[0].Inputs["approval"] != true {
		t.Fatal("response was not passed to work")
	}
}
