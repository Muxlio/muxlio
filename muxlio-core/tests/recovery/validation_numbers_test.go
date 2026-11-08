package recovery_test

import (
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
	"net/http/httptest"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type validationNumericEcho struct{}

func (validationNumericEcho) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return workers.ExecuteResult{Outputs: map[string]any{"value": req.Inputs["value"]}}, nil
}

func TestNumericValidationDiagnosticHistory(t *testing.T) {
	for _, value := range []string{`7`, `0`, `7.0`, `-0`, `9007199254740993`, `1e400`, `{"n":[1e400,9007199254740993,7,0]}`} {
		t.Run(value, func(t *testing.T) {
			s, reopen := reconciliationBackend(t, "sqlite")
			var err error
			var p plan.Plan
			if err = json.Unmarshal([]byte(`{"name":"numeric-validation","irVersion":"1.0","root":"root","subgraphs":{"root":{"nodes":[{"id":"echo","kind":"execute","executor":"test","inputs":{"value":{"expr":{"op":"input"}}},"outputs":{"value":{"schema":{"type":"string"}}}}]}}}`), &p); err != nil {
				t.Fatal(err)
			}
			engine := plan.NewEngineWithStore(&p, validationNumericEcho{}, s)
			id, err := engine.CreateRun(json.RawMessage(value))
			if err != nil {
				t.Fatal(err)
			}
			err = engine.Run(id)
			if err == nil {
				t.Fatal("expected rejected output schema")
			}
			var validation *plan.ValueValidationFailure
			if !errors.As(err, &validation) || string(validation.Actual) != value {
				t.Fatalf("typed validation error=%v", err)
			}
			// A separate pending run must remain recoverable alongside this failed run.
			pending, err := engine.CreateRun(json.RawMessage(`0`))
			if err != nil {
				t.Fatal(err)
			}
			for phase := 0; phase < 2; phase++ {
				if phase == 1 {
					s = reopen()
					engines, recoverErr := plan.RecoverRuns(s, validationNumericEcho{})
					if recoverErr != nil || engines[id] != nil || engines[pending] == nil {
						t.Fatalf("recovery=%v err=%v", engines, recoverErr)
					}
				}
				run, err := s.GetRun(id)
				if err != nil || run.State != plan.RunFailed {
					t.Fatalf("run=%+v err=%v", run, err)
				}
				activation, err := s.GetActivation(id + ":echo")
				if err != nil || activation.State != "failed" {
					t.Fatalf("activation=%+v err=%v", activation, err)
				}

				attempts, err := s.ListExecutionAttempts(id, "")
				if err != nil {
					t.Fatal(err)
				}
				if len(attempts) != 1 {
					t.Fatalf("attempts=%+v", attempts)
				}
				a := attempts[0]
				if a.State != "failed" || a.Result == nil || a.Result.Diagnostic == nil {
					t.Fatalf("expected durable failed attempt and diagnostic, state=%s result=%+v", a.State, a.Result)
				}
				if a.Result.ValidationFailure == nil || string(a.Result.ValidationFailure.Actual) != value {
					t.Fatalf("validation failure=%+v", a.Result.ValidationFailure)
				}
				diagnostics := state.AttemptDiagnostics(attempts)
				if len(diagnostics) != 1 {
					t.Fatalf("summary=%+v", diagnostics)
				}
				checkNumericJSON(t, diagnostics[0].Actual, value)
				response := httptest.NewRecorder()
				api.NewRouter(s, nil).ServeHTTP(response, httptest.NewRequest("GET", "/v1/runs/"+id, nil))
				var view struct {
					Diagnostics []diagnostic.Diagnostic `json:"diagnostics"`
				}
				if response.Code != 200 || contract.DecodeJSON(response.Body.Bytes(), &view) != nil || len(view.Diagnostics) != 1 {
					t.Fatalf("run API: %d %s", response.Code, response.Body)
				}
				checkNumericJSON(t, view.Diagnostics[0].Actual, value)
				checkNumericJSON(t, a.Result.Outputs["value"], value)
				got, err := json.Marshal(a.Result.Diagnostic.Actual)
				if err != nil || string(got) != value {
					t.Errorf("diagnostic actual=%s want %s err=%v", got, value, err)
				}
			}
		})
	}
}
