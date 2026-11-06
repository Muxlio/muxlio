package plan

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
	"strings"
	"testing"
)

func TestBadBindingDiagnosticRepair(t *testing.T) {
	raw := `{
 "irVersion":"1.0","name":"repair","root":"root",
 "subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/task",
 "inputs":{"request":{"ref":{"node":"missing","port":"value"}}}}]}}
}`
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	var id string
	for pass := 0; pass < 2; pass++ {
		found := false
		for _, d := range Validate(&p) {
			if d.Code == "MUXLIO_BAD_REF" {
				found = true
				if d.Source.Pointer != "/subgraphs/root/nodes/0/inputs/request/ref/node" || d.Source.Line != 4 || d.Source.Column == 0 || d.Fix == "" || d.Expected == nil || d.Actual == nil {
					t.Fatalf("incomplete diagnostic: %+v", d)
				}
				if pass == 0 {
					id = d.ID
				} else if id != d.ID {
					t.Fatal("unstable ID")
				}
			}
		}
		if !found {
			t.Fatal("missing binding error")
		}
	}
	fixed := strings.Replace(raw, `"inputs":{"request":{"ref":{"node":"missing","port":"value"}}}`, `"inputs":{"request":{"expr":{"op":"literal","value":"repaired"}}}`, 1)
	if err := json.Unmarshal([]byte(fixed), &p); err != nil {
		t.Fatal(err)
	}
	if HasErrors(Validate(&p)) {
		t.Fatalf("repair failed: %+v", Validate(&p))
	}
}

func TestRuntimeDiagnosticContextAndRedaction(t *testing.T) {
	t.Setenv("TEST_API_TOKEN", "runtime-private-credential")
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	node := &Node{ID: "work", Executor: "generic/task", Outputs: map[string]OutputDecl{"approval": {Schema: json.RawMessage(`{"type":"boolean"}`)}}}
	exec := workers.NewFake().ByNode("work", workers.ExecuteResult{Outputs: map[string]any{"approval": "runtime-private-credential"}})
	engine := NewEngineWithStore(&Plan{}, exec, store)
	_, err := engine.executeExecute("run", node, nil, &EvalContext{})
	if err == nil || strings.Contains(err.Error(), "runtime-private-credential") {
		t.Fatalf("unsafe runtime error: %v", err)
	}
	attempts, err := store.ListExecutionAttempts("run", "run:work")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	d := attempts[0].Result.Diagnostic
	if d == nil || d.ID == "" || d.RunID != "run" || d.ScopeID != "run:work" || d.AttemptID != "run:work-attempt-1" || d.Code != OutputValidationFailed || d.Expected == nil || d.Actual == nil || d.Fix == "" {
		t.Fatalf("incomplete runtime diagnostic: %+v", d)
	}
	b, _ := json.Marshal(attempts)
	if strings.Contains(string(b), "runtime-private-credential") {
		t.Fatalf("runtime payload leaked credential: %s", b)
	}
}

func TestSourcePositionsEscapedPointersAndMissingFields(t *testing.T) {
	raw := `{
 "irVersion":"1.0","name":"positions","root":"a/b~c",
 "subgraphs":{"a/b~c":{"nodes":[
 {"kind":"execute"},
 {"id":"work","kind":"execute","executor":"generic/task","inputs":{"x/y~z":{"ref":{"node":"missing","port":"x"}}}}
 ]}}
}`
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	for _, d := range Validate(&p) {
		switch d.Code {
		case "MUXLIO_MISSING_NODE_ID":
			if d.Source.Pointer != "/subgraphs/a~1b~0c/nodes/0/id" || d.Source.Line != 4 || d.Source.Column != 2 {
				t.Fatalf("missing field position: %+v", d.Source)
			}
		case "MUXLIO_BAD_REF":
			pointer := "/subgraphs/a~1b~0c/nodes/1/inputs/x~1y~0z/ref/node"
			before := raw[:strings.Index(raw, `"missing"`)]
			column := len(before) - strings.LastIndex(before, "\n")
			if d.Source.Pointer != pointer || d.Source.Line != 5 || d.Source.Column != column {
				t.Fatalf("reference position: %+v", d.Source)
			}
		}
	}
}

func TestRuntimeDiagnosticSourceAndStableMissingAdapter(t *testing.T) {
	raw := `{"irVersion":"1.0","name":"runtime","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/missing"}]}}}`
	var p Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	var id string
	for pass := 0; pass < 2; pass++ {
		engine := NewEngine(&p, workers.NewScriptExecutor(t.TempDir(), t.TempDir()))
		runID, err := engine.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Run(runID); err == nil {
			t.Fatal("missing adapter succeeded")
		}
		attempts, err := engine.Store().ListExecutionAttempts(runID, "")
		if err != nil || len(attempts) != 1 {
			t.Fatalf("attempts=%+v err=%v", attempts, err)
		}
		d := attempts[0].Result.Diagnostic
		if d.Code != "MUXLIO_ADAPTER_NOT_FOUND" || d.Source == nil || d.Source.Pointer != "/subgraphs/root/nodes/0" || d.Source.Line != 1 || d.AttemptID == "" {
			t.Fatalf("runtime diagnostic: %+v", d)
		}
		if pass == 0 {
			id = d.ID
		} else if id != d.ID {
			t.Fatal("runtime identifier changed across runs")
		}
	}
}

func TestDiagnosticLimitPreservesSeverity(t *testing.T) {
	for _, hardError := range []bool{false, true} {
		issues := make([]ValidationError, 150)
		for i := range issues {
			issues[i] = ValidationError{Code: "MUXLIO_WARNING", Severity: "warning", Message: "warning"}
		}
		if hardError {
			issues = append(issues, ValidationError{Code: "MUXLIO_TEST_ERROR", Severity: "error", Message: "error"})
		}
		got := finalizeDiagnostics(&Plan{}, issues)
		if len(got) != 100 || HasErrors(got) != hardError {
			t.Fatalf("severity changed when bounding: %+v", got)
		}
		if hardError && got[0].Code != "MUXLIO_TEST_ERROR" {
			t.Fatal("hard error hidden by warnings")
		}
	}
}
