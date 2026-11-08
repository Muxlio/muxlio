package recovery_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/trigger"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type artifactInputTransport func(*http.Request) (*http.Response, error)

func (f artifactInputTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoopCheckpointNumericIteration(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			s, reopen := reconciliationBackend(t, backend)
			p := exhaustedReconciliationPlan("pause")
			body := p.Subgraphs["body"]
			body.Output = map[string]plan.Binding{"value": {Expr: &plan.Expr{Op: "add", Left: &plan.Expr{Op: "state"}, Right: &plan.Expr{Op: "literal", Value: json.RawMessage(`1`)}}}}
			p.Subgraphs["body"] = body
			x := workers.NewFake()
			engine, id := createReconciliationRun(t, p, s, x)
			if err := engine.Run(id); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetActivation(id + ":loop")
			if err != nil {
				t.Fatal(err)
			}
			s = reopen()
			engines, err := plan.RecoverRuns(s, x)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetRunState(id, plan.RunRunning); err != nil {
				t.Fatal(err)
			}
			if err := engines[id].Run(id); err != nil {
				t.Fatal(err)
			}
			after, err := s.GetActivation(id + ":loop")
			if err != nil {
				t.Fatal(err)
			}
			oldJSON, _ := json.Marshal(before.Outputs)
			newJSON, _ := json.Marshal(after.Outputs)
			t.Logf("before=%s after=%s", oldJSON, newJSON)
			if string(oldJSON) != string(newJSON) {
				t.Fatalf("exhausted maxIterations=1 checkpoint changed after restart/resume")
			}
		})
	}
}

func TestNumericArtifactInputSize(t *testing.T) {
	for _, size := range []string{"7", "7.0", "7e0", "70e-1", "0", "0.0", "0e0", "-0", "8", "8.0", "-1", "1.5", "7.0000000000000000001", "9223372036854775808", "1e400", `"invalid"`, "null"} {
		t.Run(size, func(t *testing.T) {
			payload := "payload"
			expectedSize := "7"
			valid := size == "7" || size == "7.0" || size == "7e0" || size == "70e-1"
			if size == "0" || size == "0.0" || size == "0e0" || size == "-0" {
				payload, expectedSize, valid = "", "", true
			}
			mismatch := size == "8" || size == "8.0"
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(payload)))
			transfers := 0
			client := &http.Client{Transport: artifactInputTransport(func(req *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				if strings.HasSuffix(req.URL.Path, "/transfer") {
					transfers++
					if !valid && !mismatch {
						return nil, fmt.Errorf("invalid size reached transfer")
					}
					want := expectedSize
					if mismatch {
						want = "8"
					}
					if req.URL.Query().Get("size") != want {
						return nil, fmt.Errorf("transfer size=%q want %s", req.URL.Query().Get("size"), want)
					}
					json.NewEncoder(w).Encode(artifacts.TransferPlan{Digest: digest, URL: "/bytes", Method: "GET", Mode: "proxy"})
				} else {
					io.WriteString(w, payload)
				}
				return w.Result(), nil
			})}
			x := workers.NewFake()
			wrap := workers.ArtifactTransferExecutor{Inner: x, WorkBase: t.TempDir(), Client: artifacts.Client{BaseURL: "http://isolated", HTTP: client}}
			backend, _ := reconciliationBackend(t, "sqlite")
			s := backend.(*sqlite.Store)
			p := &plan.Plan{IRVersion: "1.0", Name: "artifact-size", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/task", Inputs: map[string]plan.Binding{"file": {Expr: &plan.Expr{Op: "input"}}}}}}}}
			workflowDigest, err := p.ComputeDigest()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(p)
			if err := s.SaveWorkflowVersion(workflowDigest, raw); err != nil {
				t.Fatal(err)
			}
			tr := trigger.Trigger{ID: "artifact", WorkflowDigest: workflowDigest, Secret: "0123456789abcdef", Enabled: true, InputMapping: json.RawMessage(`"{{/file}}"`)}
			if err := s.CreateTrigger(tr); err != nil {
				t.Fatal(err)
			}
			input, err := tr.MapInput([]byte(fmt.Sprintf(`{"file":{"digest":%q,"size":%s}}`, digest, size)))
			if err != nil {
				t.Fatal(err)
			}
			delivery, err := s.DeliverWebhook(trigger.WebhookDeliveryRequest{TriggerID: tr.ID, WorkflowDigest: workflowDigest, WorkflowName: p.Name, PlanJSON: raw, Input: input})
			if err != nil {
				t.Fatal(err)
			}
			runErr := plan.NewEngineWithStore(p, wrap, s).Run(delivery.RunID)
			calls := x.Calls()
			if !valid {
				if !mismatch && transfers != 0 {
					t.Fatalf("invalid size reached transfer %d times", transfers)
				}
				if mismatch && (runErr == nil || !strings.Contains(runErr.Error(), "received 7 of 8 bytes")) {
					t.Fatalf("expected size integrity failure, got %v", runErr)
				}
				if runErr == nil || len(calls) > 0 {
					t.Fatalf("invalid or mismatched size reached executor: err=%v calls=%d", runErr, len(calls))
				}
			} else {
				if runErr != nil || len(calls) != 1 {
					t.Fatalf("run err=%v calls=%d", runErr, len(calls))
				}
				want := int64(len(payload))
				if got := calls[0].Inputs["file"].(map[string]any)["size"]; got != want {
					t.Fatalf("materialized size=%v want %d", got, want)
				}
			}
		})
	}
}

type failingNumericEcho struct{ value any }

func (e failingNumericEcho) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return workers.ExecuteResult{Outputs: map[string]any{"value": e.value}, Error: "generic failure"}, nil
}
func TestNumericCreateRunAndFailedAttempt(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"0"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		t.Run(value, func(t *testing.T) {
			backend, _ := reconciliationBackend(t, "sqlite")
			s := backend.(*sqlite.Store)
			p := &plan.Plan{IRVersion: "1.0", Name: "numeric", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "echo", Kind: plan.KindExecute, Executor: "test"}}}}}
			engine := plan.NewEngineWithStore(p, workers.NewFake(), s)
			id, err := engine.CreateRun(json.RawMessage(value))
			if err != nil {
				t.Error("create:", err)
			} else {
				r, err := s.GetRun(id)
				if err != nil {
					t.Fatal(err)
				}
				checkNumericJSON(t, r.Input, value)
			}
		})
	}
	// Probe failed-result persistence independently of CreateRun's input decode.
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"0"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		t.Run("failure/"+value, func(t *testing.T) {
			backend, _ := reconciliationBackend(t, "sqlite")
			s := backend.(*sqlite.Store)
			var input any
			d := json.NewDecoder(strings.NewReader(value))
			d.UseNumber()
			if err := d.Decode(&input); err != nil {
				t.Fatal(err)
			}
			p := &plan.Plan{IRVersion: "1.0", Name: "numeric", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "echo", Kind: plan.KindExecute, Executor: "test"}}}}}
			engine := plan.NewEngineWithStore(p, failingNumericEcho{input}, s)
			id, err := engine.CreateRun(json.RawMessage(`0`))
			if err != nil {
				t.Fatal(err)
			}
			if err = engine.Run(id); err == nil {
				t.Fatal("failure was lost")
			}
			a, err := s.ListExecutionAttempts(id, "")
			if err != nil || len(a) != 1 || a[0].Result == nil {
				t.Fatalf("attempts=%+v err=%v", a, err)
			}
			checkNumericJSON(t, a[0].Result.Outputs["value"], value)
		})
	}
}
