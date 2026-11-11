package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/trigger"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestWebhookNumericInputExecutionAndRecovery(t *testing.T) {
	for _, comparison := range []string{"", "eq", "ne"} {
		for _, number := range []string{"9007199254740993", "1e400", "7", "0"} {
			for _, mode := range []string{"reference", "nested-reference", "passthrough", "nested-passthrough", "literal", "nested-literal"} {
				t.Run(comparison+"/"+number+"/"+mode, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "webhooks.db")
					s, err := sqlite.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { s.Close() }()
					nested := mode == "nested-reference" || mode == "nested-passthrough" || mode == "nested-literal"
					want := number
					schema := `{"type":"number"}`
					if nested {
						want = `{"values":[` + number + `]}`
						schema = `{"type":"object"}`
					}
					var p plan.Plan
					raw := `{"name":"webhook-numbers","irVersion":"1.0","root":"root","inputSchema":` + schema + `,"subgraphs":{"root":{"nodes":[{"id":"task","kind":"execute","executor":"test/task","inputs":{"value":{"expr":{"op":"input"}}}}]}}}`
					if err := json.Unmarshal([]byte(raw), &p); err != nil {
						t.Fatal(err)
					}
					sg := p.Subgraphs["root"]
					sg.Output = map[string]plan.Binding{"value": {Expr: &plan.Expr{Op: "input"}}}
					p.Subgraphs["root"] = sg
					if comparison != "" {
						node := &p.Subgraphs["root"].Nodes[0]
						right := &plan.Expr{Op: "literal", Value: json.RawMessage(want)}
						// Plan digests use ECMAScript numbers. Keep out-of-range
						// values in run input, and compare them with themselves.
						if number == "1e400" {
							right = &plan.Expr{Op: "input"}
						}
						node.Inputs["value"] = plan.Binding{Expr: &plan.Expr{Op: comparison, Left: &plan.Expr{Op: "input"}, Right: right}}
					}
					expectedValue := func(input string) string {
						if comparison == "" {
							return input
						}
						equal := input == want || number == "1e400"
						if comparison == "ne" {
							equal = !equal
						}
						if equal {
							return "true"
						}
						return "false"
					}
					digest, err := p.ComputeDigest()
					if err != nil {
						t.Fatal(err)
					}
					pinned, _ := json.Marshal(p)
					if err := s.SaveWorkflowVersion(digest, pinned); err != nil {
						t.Fatal(err)
					}
					const secret = "0123456789abcdef"
					body := []byte(want)
					definition := map[string]any{"id": "numbers", "workflowDigest": digest, "secret": secret}
					switch mode {
					case "reference", "nested-reference":
						definition["inputMapping"] = "{{/n}}"
						body = []byte(`{"n":` + want + `}`)
					case "literal", "nested-literal":
						definition["inputMapping"] = json.RawMessage(want)
						body = []byte(`null`)
					}
					x := workers.NewFake()
					runner := &serverRunner{store: s, exec: x, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}, ctx: context.Background()}
					router := api.NewRouter(s, runner)
					post := func(handler http.Handler, path string, payload []byte, key string, status int) string {
						t.Helper()
						req := httptest.NewRequest("POST", path, bytes.NewReader(payload))
						mac := hmac.New(sha256.New, []byte(secret))
						mac.Write(payload)
						req.Header.Set("X-Muxlio-Signature", hex.EncodeToString(mac.Sum(nil)))
						req.Header.Set("Idempotency-Key", key)
						rec := httptest.NewRecorder()
						handler.ServeHTTP(rec, req)
						if rec.Code != status {
							t.Fatalf("%s: %d want %d: %s", path, rec.Code, status, rec.Body.String())
						}
						var response struct {
							RunID string `json:"runId"`
						}
						json.Unmarshal(rec.Body.Bytes(), &response)
						return response.RunID
					}
					def, _ := json.Marshal(definition)
					post(router, "/v1/triggers", def, "", 201)
					first := post(router, "/webhooks/numbers", body, "first", 201)
					runner.wg.Wait()
					assertRunOutput(t, s, first, want)
					if id := post(router, "/webhooks/numbers", body, "first", 200); id != first {
						t.Fatal("duplicate changed run")
					}
					if calls := x.Calls(); len(calls) != 1 {
						t.Fatalf("calls=%d", len(calls))
					} else {
						got, err := json.Marshal(calls[0].Inputs["value"])
						if err != nil || string(got) != expectedValue(want) {
							t.Fatalf("executor input=%s want %s: %v", got, expectedValue(want), err)
						}
					}
					// Leave a committed delivery unattached, simulating a coordinator crash.
					pending := post(api.NewRouter(s, nil), "/webhooks/numbers", body, "pending", 201)
					ordinary := "7"
					if nested {
						ordinary = `{"values":[7]}`
					}
					unrelated, err := plan.NewEngineWithStore(&p, x, s).CreateRun(json.RawMessage(ordinary))
					if err != nil {
						t.Fatal(err)
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = sqlite.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					if runs, err := s.ListRuns(); err != nil || len(runs) != 3 {
						t.Fatalf("list after reopen: %d %v", len(runs), err)
					}
					assertRunOutput(t, s, first, want)
					if id := post(api.NewRouter(s, nil), "/webhooks/numbers", body, "pending", 200); id != pending {
						t.Fatal("retry changed run")
					}
					recovered := workers.NewFake()
					engines, err := plan.RecoverRuns(s, recovered)
					if err != nil {
						t.Fatal(err)
					}
					if len(engines) != 2 || engines[pending] == nil || engines[unrelated] == nil {
						t.Fatalf("recovered engines=%v", engines)
					}
					for id, engine := range engines {
						if err := engine.Run(id); err != nil {
							t.Fatal(err)
						}
					}
					calls := recovered.Calls()
					if len(calls) != 2 {
						t.Fatalf("recovered calls=%d", len(calls))
					}
					for _, call := range calls {
						expected := want
						if call.RunID == unrelated {
							expected = ordinary
						}
						assertRunOutput(t, s, call.RunID, expected)
						got, err := json.Marshal(call.Inputs["value"])
						if err != nil || string(got) != expectedValue(expected) {
							t.Fatalf("recovered input=%s want %s: %v", got, expectedValue(expected), err)
						}
					}
				})
			}
		}
	}
}

func assertRunOutput(t *testing.T, s *sqlite.Store, id, want string) {
	t.Helper()
	run, err := s.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != plan.RunSucceeded {
		t.Fatalf("run state=%s", run.State)
	}
	assertExactJSON(t, run.Output, want)
}

func assertExactJSON(t *testing.T, value any, want string) {
	t.Helper()
	got, err := json.Marshal(value)
	if err != nil || string(got) != want {
		t.Fatalf("JSON=%s want %s: %v", got, want, err)
	}
}

// Pause after a durable project checkpoint to model interruption before completion.
type pauseAfterProjectStore struct {
	*sqlite.Store
	runID string
}

func (s *pauseAfterProjectStore) SetActivationOutputs(id string, outputs map[string]any) error {
	if err := s.Store.SetActivationOutputs(id, outputs); err != nil {
		return err
	}
	return s.Store.SetRunState(s.runID, plan.RunPaused)
}
func TestWebhookNumericActivationCheckpointRecovery(t *testing.T) {
	for _, number := range []string{`9007199254740993`, `1e400`, `7`, `0`, `{"values":[9007199254740993,1e400,7,0]}`} {
		t.Run(number, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checkpoint.db")
			s, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			var p plan.Plan
			if err := json.Unmarshal([]byte(`{"name":"checkpoint","irVersion":"1.0","root":"root","subgraphs":{"root":{"nodes":[{"id":"project","kind":"project","inputs":{"value":{"expr":{"op":"input"}}}},{"id":"task","kind":"execute","executor":"test/task","inputs":{"value":{"ref":{"node":"project","port":"value"}}}}],"output":{"value":{"expr":{"op":"literal","value":0}}}}}}`), &p); err != nil {
				t.Fatal(err)
			}
			digest, err := p.ComputeDigest()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(p)
			if err := s.SaveWorkflowVersion(digest, raw); err != nil {
				t.Fatal(err)
			}
			tr := trigger.Trigger{ID: "checkpoint", WorkflowDigest: digest, Secret: "0123456789abcdef", Enabled: true}
			if err := s.CreateTrigger(tr); err != nil {
				t.Fatal(err)
			}
			result, err := s.DeliverWebhook(trigger.WebhookDeliveryRequest{TriggerID: tr.ID, WorkflowDigest: digest, WorkflowName: p.Name, PlanJSON: raw, Input: json.RawMessage(number), DeduplicationSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			x := workers.NewFake()
			engine := plan.NewEngineWithStore(&p, x, &pauseAfterProjectStore{Store: s, runID: result.RunID})
			if err := engine.Run(result.RunID); err != nil {
				t.Fatal(err)
			}
			if len(x.Calls()) != 0 {
				t.Fatal("dependent executor ran before restart")
			}
			run, err := s.GetRun(result.RunID)
			if err != nil || run.State != plan.RunPaused {
				t.Fatalf("checkpoint run=%+v: %v", run, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			act, readErr := s.GetActivation(result.RunID + ":project")
			engines, recoveryErr := plan.RecoverRuns(s, x)
			if readErr != nil || recoveryErr != nil {
				t.Fatalf("after project checkpoint/reopen: GetActivation=%v RecoverRuns=%v", readErr, recoveryErr)
			}
			if act.State != plan.ActSucceeded {
				t.Fatalf("checkpoint state=%s", act.State)
			}
			got, err := json.Marshal(act.Outputs["value"])
			if err != nil || string(got) != number {
				t.Fatalf("checkpoint output=%s want %s: %v", got, number, err)
			}
			resumed := engines[result.RunID]
			if resumed == nil {
				t.Fatal("checkpoint not recovered")
			}
			if err := s.SetRunState(result.RunID, plan.RunRunning); err != nil {
				t.Fatal(err)
			}
			if err := resumed.Run(result.RunID); err != nil {
				t.Fatal(err)
			}
			calls := x.Calls()
			if len(calls) != 1 {
				t.Fatalf("resumed calls=%d", len(calls))
			}
			assertExactJSON(t, calls[0].Inputs["value"], number)
			assertRunOutput(t, s, result.RunID, "0")
		})
	}
}

// Stop at the durable attempt boundary, before activation output checkpointing.
type stopAfterAttemptStore struct{ *sqlite.Store }

func (s stopAfterAttemptStore) RecordExecutionAttempt(a state.ExecutionAttempt) error {
	if err := s.Store.RecordExecutionAttempt(a); err != nil {
		return err
	}
	if a.State == "succeeded" {
		panic("test coordinator stopped after attempt commit")
	}
	return nil
}

type numericEchoExecutor struct{ calls int }

func (x *numericEchoExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	x.calls++
	return workers.ExecuteResult{Outputs: map[string]any{"value": req.Inputs["value"]}}, nil
}

func TestWebhookNumericAttemptOutputs(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `{"values":[9007199254740993,1e400,7,0]}`} {
		for _, mode := range []string{"complete", "resume", "retry"} {
			t.Run(value+"/"+mode, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "attempts.db")
				s, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { s.Close() }()
				var p plan.Plan
				if err := json.Unmarshal([]byte(`{"name":"echo","irVersion":"1.0","root":"root","limits":{},"subgraphs":{"root":{"nodes":[{"id":"echo","kind":"execute","executor":"test/echo","inputs":{"value":{"expr":{"op":"input"}}}}],"output":{"value":{"ref":{"node":"echo","port":"value"}}}}}}`), &p); err != nil {
					t.Fatal(err)
				}
				digest, err := p.ComputeDigest()
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(p)
				if err := s.SaveWorkflowVersion(digest, raw); err != nil {
					t.Fatal(err)
				}
				tr := trigger.Trigger{ID: "echo", WorkflowDigest: digest, Secret: "0123456789abcdef", Enabled: true}
				if err := s.CreateTrigger(tr); err != nil {
					t.Fatal(err)
				}
				delivery, err := s.DeliverWebhook(trigger.WebhookDeliveryRequest{TriggerID: tr.ID, WorkflowDigest: digest, WorkflowName: p.Name, PlanJSON: raw, Input: json.RawMessage(value)})
				if err != nil {
					t.Fatal(err)
				}
				x := &numericEchoExecutor{}
				if mode == "complete" {
					if err := plan.NewEngineWithStore(&p, x, s).Run(delivery.RunID); err != nil {
						t.Fatal(err)
					}
				} else {
					func() {
						defer func() {
							if got := recover(); got != "test coordinator stopped after attempt commit" {
								t.Fatalf("unexpected stop: %v", got)
							}
						}()
						if err := plan.NewEngineWithStore(&p, x, stopAfterAttemptStore{s}).Run(delivery.RunID); err != nil {
							t.Fatal(err)
						}
					}()
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				attempts, err := s.ListExecutionAttempts(delivery.RunID, "")
				if err != nil || len(attempts) != 1 {
					t.Fatalf("attempts=%v err=%v", attempts, err)
				}
				assertExactJSON(t, attempts[0].Result.Outputs["value"], value)
				if attempts[0].Accounting != nil {
					t.Fatal("missing usage became available")
				}
				switch mode {
				case "resume":
					engines, err := plan.RecoverRuns(s, x)
					if err != nil {
						t.Fatal(err)
					}
					if engines[delivery.RunID] == nil {
						t.Fatal("missing recovered engine")
					}
					if err := engines[delivery.RunID].Run(delivery.RunID); err != nil {
						t.Fatal(err)
					}
				case "retry":
					if err := s.SetActivationError(delivery.RunID+":echo", fmt.Errorf("interrupted")); err != nil {
						t.Fatal(err)
					}
					if err := s.SetRunError(delivery.RunID, fmt.Errorf("interrupted")); err != nil {
						t.Fatal(err)
					}
					engine := plan.NewEngineWithStore(&p, x, s)
					if err := engine.Retry(delivery.RunID); err != nil {
						t.Fatal(err)
					}
					if err := engine.Run(delivery.RunID); err != nil {
						t.Fatal(err)
					}
				}
				run, err := s.GetRun(delivery.RunID)
				if err != nil || run.State != plan.RunSucceeded {
					t.Fatalf("run=%+v err=%v", run, err)
				}
				assertExactJSON(t, run.Output, value)
				if x.calls != 1 {
					t.Fatalf("echo was replayed: %d calls", x.calls)
				}
				after, err := s.ListExecutionAttempts(delivery.RunID, "")
				if err != nil || len(after) != 1 {
					t.Fatalf("attempt history changed: %v %v", after, err)
				}
			})
		}
	}
}
