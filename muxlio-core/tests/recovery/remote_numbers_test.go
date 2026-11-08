package recovery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/trigger"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// This subprocess is a generic adapter using the actual request, scalar-output,
// and result libraries. Worker unit fixtures do not import adapter internals.
func TestNumericAdapterProcess(t *testing.T) {
	if os.Getenv("MUXLIO_NUMERIC_ADAPTER") != "1" {
		return
	}
	req, err := adapter.ReadRequest()
	if err != nil {
		adapter.Fail("%v", err)
	}
	raw, err := json.Marshal(req.Inputs["value"])
	if err != nil {
		adapter.Fail("%v", err)
	}
	if err = os.WriteFile(filepath.Join(req.WorkDir, "value"), raw, 0600); err != nil {
		adapter.Fail("%v", err)
	}
	outputs, _, errs := adapter.CollectOutputs(req)
	if req.Task == "pathless" {
		delete(outputs, "value")
	}
	outputs["redact"] = req.Secrets["key"]
	var usage *contract.TokenUsage
	if req.Task == "pathless" {
		usage = &contract.TokenUsage{}
	}
	var measurements *contract.Measurements
	if req.Task == "reported" {
		usage = &contract.TokenUsage{Input: 9007199254740993, Output: 7, Cached: 0}
		cost := 0.25
		measurements = &contract.Measurements{CostUSD: &cost}
	}
	if err = adapter.WriteResponse(adapter.Response{Outputs: outputs, OutputErrors: errs, Usage: usage, Measurements: measurements}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type numericAttemptStop struct{ *sqlite.Store }

func (s numericAttemptStop) RecordExecutionAttempt(a state.ExecutionAttempt) error {
	if err := s.Store.RecordExecutionAttempt(a); err != nil {
		return err
	}
	if a.State == "succeeded" {
		panic("numeric attempt committed")
	}
	return nil
}

type numericCallCounter struct {
	inner workers.Executor
	calls atomic.Int64
}

func (c *numericCallCounter) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	c.calls.Add(1)
	return c.inner.Execute(req)
}

func TestRemoteNumericEchoRecovery(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapterDir := t.TempDir()
	wrapper := "#!/bin/sh\nexport MUXLIO_NUMERIC_ADAPTER=1\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run '^TestNumericAdapterProcess$'\n"
	if err = os.WriteFile(filepath.Join(adapterDir, "test"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(adapterDir, "test.json"), []byte(`{"manifestVersion":"1","executor":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUXLIO_NUMERIC_SECRET", "actual-credential-with-\"quote\nnewline")
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"9007199254740993"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		for _, secret := range []bool{false, true} {
			for _, mode := range []string{"collected", "pathless", "reported"} {
				t.Run(fmt.Sprintf("%s/secrets=%v/%s", value, secret, mode), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "echo.db")
					s, err := sqlite.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { s.Close() }()
					var p plan.Plan
					if err = json.Unmarshal([]byte(`{"name":"remote-numbers","irVersion":"1.0","root":"root","limits":{},"subgraphs":{"root":{"nodes":[{"id":"echo","kind":"execute","executor":"test","inputs":{"value":{"expr":{"op":"input"}}},"outputs":{"value":{}}}],"output":{"value":{"ref":{"node":"echo","port":"value"}}}}}}`), &p); err != nil {
						t.Fatal(err)
					}
					node := &p.Subgraphs["root"].Nodes[0]
					node.Task = mode
					if secret {
						node.Params = json.RawMessage(`{"secretRefs":{"key":"handle"},"executionPolicy":{"allowedSecrets":["key"]}}`)
					}
					digest, err := p.ComputeDigest()
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(p)
					if err != nil {
						t.Fatal(err)
					}
					if err = s.SaveWorkflowVersion(digest, raw); err != nil {
						t.Fatal(err)
					}
					tr := trigger.Trigger{ID: "numbers", WorkflowDigest: digest, Secret: "0123456789abcdef", Enabled: true, InputMapping: json.RawMessage(`"{{/n}}"`)}
					if err = s.CreateTrigger(tr); err != nil {
						t.Fatal(err)
					}
					input, err := tr.MapInput([]byte(`{"n":` + value + `}`))
					if err != nil {
						t.Fatal(err)
					}
					delivery, err := s.DeliverWebhook(trigger.WebhookDeliveryRequest{TriggerID: tr.ID, WorkflowDigest: digest, WorkflowName: p.Name, PlanJSON: raw, Input: input})
					if err != nil {
						t.Fatal(err)
					}
					coordinator := api.NewCoordinator(s)
					handler := api.NewWorkerProtocolHandler(coordinator)
					client := &http.Client{Transport: artifactInputTransport(func(r *http.Request) (*http.Response, error) {
						w := httptest.NewRecorder()
						handler.ServeHTTP(w, r)
						return w.Result(), nil
					})}
					script := workers.NewScriptExecutor(adapterDir, t.TempDir())
					script.Executors = map[string]workers.ExecutorConfig{"test": {SecretEnv: map[string]string{"handle": "MUXLIO_NUMERIC_SECRET"}}}
					counter := &numericCallCounter{inner: workers.ArtifactTransferExecutor{Inner: script, WorkBase: t.TempDir()}}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					stopped := make(chan error, 1)
					go func() {
						stopped <- workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{CoordinatorURL: "http://isolated", WorkerID: "worker", Capacity: 1, Capabilities: []workers.RemoteCapability{{Name: "test"}}, HTTPClient: client, PollInterval: time.Millisecond, HeartbeatInterval: time.Millisecond, WorkBase: t.TempDir()}, counter)
					}()
					engine := plan.NewEngineWithStore(&p, coordinator, numericAttemptStop{s})
					engine.Context = ctx
					func() {
						defer func() {
							if got := recover(); got != "numeric attempt committed" {
								t.Errorf("unexpected stop: %v", got)
							}
						}()
						if err := engine.Run(delivery.RunID); err != nil {
							t.Error(err)
						}
					}()
					cancel()
					if err := <-stopped; err != nil {
						t.Fatal(err)
					}
					attempts, err := s.ListExecutionAttempts(delivery.RunID, "")
					if err != nil || len(attempts) != 1 || attempts[0].Result == nil {
						t.Fatalf("attempts=%+v err=%v", attempts, err)
					}
					result := attempts[0].Result
					checkNumericJSON(t, result.Outputs["value"], value)
					if secret && result.Outputs["redact"] != "[REDACTED]" {
						t.Fatalf("credential not redacted: %+v", result.Outputs)
					}
					if (attempts[0].Usage == nil) != (mode == "collected") {
						t.Fatalf("usage availability changed: %+v", attempts[0].Usage)
					}
					wantUsage := state.TokenUsage{}
					if mode == "reported" {
						wantUsage = state.TokenUsage{Input: 9007199254740993, Output: 7}
					}
					if attempts[0].Usage != nil && *attempts[0].Usage != wantUsage {
						t.Fatalf("reported usage changed: %+v", attempts[0].Usage)
					}
					if mode == "reported" && (attempts[0].Measurements == nil || attempts[0].Measurements.CostUSD == nil || *attempts[0].Measurements.CostUSD != 0.25) {
						t.Fatalf("typed measurements changed: %+v", attempts[0].Measurements)
					}
					// Reopen and recover both the queued completion and the successful attempt.
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = sqlite.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					replay := api.NewCoordinator(s)
					r, err := replay.Execute(workers.ExecuteRequest{RunID: delivery.RunID, ActivationID: delivery.RunID + ":echo", AttemptID: attempts[0].ID, Executor: "test"})
					if err != nil {
						t.Fatal(err)
					}
					checkNumericJSON(t, r.Outputs["value"], value)
					if (r.Usage == nil) != (mode == "collected") || (r.Usage != nil && *r.Usage != wantUsage) {
						t.Fatalf("recovered usage changed: %+v", r.Usage)
					}
					engines, err := plan.RecoverRuns(s, replay)
					if err != nil {
						t.Fatal(err)
					}
					if engines[delivery.RunID] == nil {
						t.Fatal("missing recovered run")
					}
					if err = engines[delivery.RunID].Run(delivery.RunID); err != nil {
						t.Fatal(err)
					}
					run, err := s.GetRun(delivery.RunID)
					if err != nil || run.State != plan.RunSucceeded {
						t.Fatalf("run=%+v err=%v", run, err)
					}
					checkNumericJSON(t, run.Output, value)
					if counter.calls.Load() != 1 {
						t.Fatalf("execution count=%d", counter.calls.Load())
					}
				})
			}
		}
	}
}
func checkNumericJSON(t *testing.T, value any, want string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil || string(raw) != want {
		t.Fatalf("value=%s want %s err=%v", raw, want, err)
	}
}
