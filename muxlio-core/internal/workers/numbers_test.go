package workers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestGenericExactNumberBoundaries(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"0"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		t.Run(value, func(t *testing.T) {
			raw := []byte(`{"executor":"test","inputs":{"value":` + value + `}}`)
			safe, err := SanitizeExecuteJSON(raw)
			if err != nil {
				t.Error(err)
			} else {
				var req struct{ Inputs map[string]json.RawMessage }
				if err := json.Unmarshal(safe, &req); err != nil {
					t.Fatal(err)
				}
				if string(req.Inputs["value"]) != value {
					t.Errorf("artifact request=%s", safe)
				}
			}
			if !MatchWorkerDispatch("worker", "test", raw, []state.WorkerCapability{{Name: "test"}}, nil) {
				t.Error("dispatch rejected numeric request")
			}
			for _, secrets := range []map[string]string{nil, {"key": "real-secret"}} {
				var input any
				d := json.NewDecoder(strings.NewReader(value))
				d.UseNumber()
				if err := d.Decode(&input); err != nil {
					t.Fatal(err)
				}
				for _, usage := range []*contract.TokenUsage{nil, {}, {Input: 9007199254740993, Output: 7}} {
					cost := 0.25
					r, err := sanitizeExecution(ExecuteResult{Outputs: map[string]any{"value": input, "redact": "real-secret"}, Usage: usage, Measurements: &contract.Measurements{CostUSD: &cost}}, nil, secrets)
					got, _ := json.Marshal(r.Outputs["value"])
					if err != nil || string(got) != value {
						t.Errorf("sanitize=%s err=%v", got, err)
					}
					want := "real-secret"
					if len(secrets) > 0 {
						want = "[REDACTED]"
					}
					if r.Outputs["redact"] != want || (r.Usage == nil) != (usage == nil) {
						t.Error("redaction or usage changed")
					}
					if usage != nil && (r.Usage == nil || *r.Usage != *usage) {
						t.Fatalf("typed usage changed: got %+v want %+v", r.Usage, usage)
					}
					if r.Measurements == nil || r.Measurements.CostUSD == nil || *r.Measurements.CostUSD != cost {
						t.Fatalf("typed measurements changed: %+v", r.Measurements)
					}
				}
			}
			script := testScript(t, "printf '%s' '"+value+"' > value; printf '{}' ")
			r, err := script.Execute(ExecuteRequest{Executor: "test", Outputs: map[string]OutputDecl{"value": {}}})
			got, _ := json.Marshal(r.Outputs["value"])
			if err != nil || r.Error != "" || string(got) != value {
				t.Errorf("pathless=%s result=%+v err=%v", got, r, err)
			}
		})
	}
}

func TestNumericRequestAndPathlessDocumentValidation(t *testing.T) {
	for _, suffix := range []string{` {}`, ` trailing`} {
		raw := []byte(`{"inputs":{"value":9007199254740993}}` + suffix)
		if _, err := SanitizeExecuteJSON(raw); err == nil {
			t.Error("accepted trailing request")
		}
		if MatchWorkerDispatch("worker", "test", raw, []state.WorkerCapability{{Name: "test"}}, nil) {
			t.Error("matched trailing request")
		}
		script := testScript(t, `printf '%s' '9007199254740993`+suffix+`' > value; printf '{}'`)
		r, err := script.Execute(ExecuteRequest{Executor: "test", Outputs: map[string]OutputDecl{"value": {}}})
		if err != nil || r.OutputErrors["value"] == "" {
			t.Fatalf("accepted trailing pathless output: %+v %v", r, err)
		}
	}
}
