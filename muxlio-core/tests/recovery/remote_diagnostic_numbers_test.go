package recovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type numericDiagnosticExecutor struct{}

func (numericDiagnosticExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	d := diagnostic.New("MUXLIO_TEST_NUMBER", "runtime", "password=private-value", "repair the input")
	d.Actual = req.Inputs["value"]
	d.Expected = req.Inputs["value"]
	d.Requirements = map[string]any{"value": req.Inputs["value"], "password": "private-value"}
	d.Source = &diagnostic.Source{Pointer: "/value"}
	cause := diagnostic.New("MUXLIO_TEST_CAUSE", "runtime", "nested failure", "")
	cause.Actual = req.Inputs["value"]
	d.Causes = []diagnostic.Diagnostic{cause}
	d.Identify()
	return workers.ExecuteResult{Outputs: map[string]any{"value": req.Inputs["value"]}}, &d
}

func TestRemoteNumericDiagnosticRecovery(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `-0`, `70e-1`, `"0"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "diagnostics.db")
			s, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			var p plan.Plan
			if err = json.Unmarshal([]byte(`{"name":"numeric-failure","irVersion":"1.0","root":"root","subgraphs":{"root":{"nodes":[{"id":"fail","kind":"execute","executor":"test","inputs":{"value":{"expr":{"op":"input"}}}}]}}}`), &p); err != nil {
				t.Fatal(err)
			}
			coordinator := api.NewCoordinator(s)
			handler := api.NewWorkerProtocolHandler(coordinator)
			completed := make(chan struct{}, 1)
			client := &http.Client{Transport: artifactInputTransport(func(r *http.Request) (*http.Response, error) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				// Require the real completion handler to accept the failed result.
				if strings.HasSuffix(r.URL.Path, "/complete") && w.Code == http.StatusAccepted {
					completed <- struct{}{}
				}
				return w.Result(), nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stopped := make(chan error, 1)
			go func() {
				stopped <- workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{CoordinatorURL: "http://isolated", WorkerID: "worker", Capacity: 1, Capabilities: []workers.RemoteCapability{{Name: "test"}}, HTTPClient: client, PollInterval: time.Millisecond, HeartbeatInterval: time.Millisecond, WorkBase: t.TempDir()}, numericDiagnosticExecutor{})
			}()
			engine := plan.NewEngineWithStore(&p, coordinator, s)
			engine.Context = ctx
			id, err := engine.CreateRun(json.RawMessage(value))
			if err != nil {
				cancel()
				<-stopped
				t.Fatal(err)
			}
			if err := engine.Run(id); err == nil {
				t.Error("expected executor failure")
			}
			cancel()
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			select {
			case <-completed:
			default:
				t.Fatal("no accepted completion")
			}
			for phase := 0; phase < 2; phase++ {
				if phase == 1 {
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = sqlite.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					engines, err := plan.RecoverRuns(s, api.NewCoordinator(s))
					if err != nil || engines[id] != nil {
						t.Fatalf("failed run recovery=%v err=%v", engines, err)
					}
				}
				attempts, err := s.ListExecutionAttempts(id, "")
				if err != nil || len(attempts) != 1 || attempts[0].Result == nil {
					t.Fatalf("attempts=%+v err=%v", attempts, err)
				}
				a := attempts[0]
				result, execErr := api.NewCoordinator(s).Execute(workers.ExecuteRequest{Context: context.Background(), Executor: "test", RunID: id, ActivationID: a.ActivationID, AttemptID: a.ID})
				var d *diagnostic.Diagnostic
				if !errors.As(execErr, &d) {
					t.Fatalf("lost typed diagnostic: result=%+v err=%v", result, execErr)
				}
				checkNumericDiagnostic(t, d, value, false)
				checkNumericJSON(t, result.Outputs["value"], value)
				if result.Usage != nil || a.Usage != nil {
					t.Fatal("unavailable usage changed")
				}
				checkNumericDiagnostic(t, a.Result.Diagnostic, value, true)
				diagnostics := state.AttemptDiagnostics(attempts)
				if len(diagnostics) != 1 {
					t.Fatalf("summary=%+v", diagnostics)
				}
				checkNumericDiagnostic(t, &diagnostics[0], value, true)
				response := httptest.NewRecorder()
				api.NewRouter(s, nil).ServeHTTP(response, httptest.NewRequest("GET", "/v1/runs/"+id, nil))
				var view struct {
					Diagnostics []diagnostic.Diagnostic `json:"diagnostics"`
				}
				if response.Code != 200 || contract.DecodeJSON(response.Body.Bytes(), &view) != nil || len(view.Diagnostics) != 1 {
					t.Fatalf("run API: %d %s", response.Code, response.Body)
				}
				checkNumericDiagnostic(t, &view.Diagnostics[0], value, true)
			}
		})
	}
}

func checkNumericDiagnostic(t *testing.T, d *diagnostic.Diagnostic, value string, attempt bool) {
	t.Helper()
	original := diagnostic.New("MUXLIO_TEST_NUMBER", "runtime", "", "")
	pointer := "/value"
	if attempt {
		pointer = "/subgraphs/root/nodes/0"
		original.NodeID = "fail"
		original.Subgraph = "root"
	}
	original.Source = &diagnostic.Source{Pointer: pointer}
	original.Identify()
	if d == nil || d.ID != original.ID || d.Code != original.Code || d.Phase != "runtime" || d.Severity != "error" || d.Fix != "repair the input" || d.Source == nil || d.Source.Pointer != pointer {
		t.Fatalf("diagnostic fields changed: %+v", d)
	}
	checkNumericJSON(t, d.Actual, value)
	checkNumericJSON(t, d.Expected, value)
	requirements, ok := d.Requirements.(map[string]any)
	if !ok || requirements["password"] != "[REDACTED]" || strings.Contains(d.Message, "private-value") {
		t.Fatalf("redaction lost: %+v", d)
	}
	checkNumericJSON(t, requirements["value"], value)
	if len(d.Causes) < 1 {
		t.Fatalf("causes=%+v", d.Causes)
	}
	checkNumericJSON(t, d.Causes[0].Actual, value)
}
