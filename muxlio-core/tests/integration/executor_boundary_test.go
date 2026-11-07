package integration_test

import (
	"context"
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteCompletionPreservesOutputValidationErrors(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	c := api.NewCoordinator(s)
	server := httptest.NewServer(api.NewWorkerProtocolHandler(c))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{CoordinatorURL: server.URL, WorkerID: "worker", Capacity: 1, Capabilities: []workers.RemoteCapability{{Name: "test"}}, PollInterval: time.Millisecond, HeartbeatInterval: 10 * time.Millisecond}, workers.NewFake().ByExecutor("test", workers.ExecuteResult{Outputs: map[string]any{"value": "plain prose"}, OutputErrors: map[string]string{"value": "invalid JSON"}}))
	}()
	result, err := c.Execute(workers.ExecuteRequest{Context: ctx, Executor: "test", RunID: "run", ActivationID: "run:node", AttemptID: "attempt"})
	cancel()
	<-done
	if err != nil || result.OutputErrors["value"] != "invalid JSON" {
		t.Fatalf("output errors lost: %+v %v", result, err)
	}
	if err := plan.ValidateExecuteResult(map[string]plan.OutputDecl{"value": {Schema: json.RawMessage(`{"type":"string"}`)}}, result); err == nil {
		t.Fatal("invalid JSON accepted remotely")
	}
}

func TestA2AFileOutputPublishesThroughGenericWorker(t *testing.T) {
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "agent-a2a"), "./cmd/agent-a2a")
	build.Dir = "../../../muxlio-adapters"
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	manifest, err := os.ReadFile("../../../muxlio-adapters/cmd/agent-a2a/adapter.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-a2a.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	var endpoint string
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/transfer") {
			json.NewEncoder(w).Encode(artifacts.TransferPlan{Digest: strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/artifacts/"), "/transfer"), URL: "/blob", Method: "PUT", Mode: "proxy"})
			return
		}
		if r.Method == "PUT" {
			uploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
			return
		}
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(map[string]any{"protocolVersion": "0.3.0", "url": endpoint, "defaultInputModes": []string{"text/plain"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "muxlio", "result": map[string]any{"kind": "task", "id": "remote", "contextId": "context", "status": map[string]any{"state": "completed"}, "artifacts": []any{map[string]any{"name": "report", "parts": []any{map[string]any{"kind": "text", "text": "report contents"}}}}}})
	}))
	defer server.Close()
	endpoint = server.URL + "/rpc"
	params, _ := json.Marshal(map[string]any{"url": server.URL})
	runner := workers.ArtifactTransferExecutor{Inner: workers.NewScriptExecutor(dir, t.TempDir()), WorkBase: t.TempDir(), Client: artifacts.Client{BaseURL: server.URL}}
	result, err := runner.Execute(workers.ExecuteRequest{Executor: "agent/a2a", Params: params, RequiredCapabilities: map[string]string{contract.CapabilityRemoteTask: contract.Version}, Outputs: map[string]workers.OutputDecl{"report": {Path: "report.txt", Mime: "text/plain"}}})
	if err != nil || result.Error != "" || string(uploaded) != "report contents" {
		t.Fatalf("file publication: %+v %v uploaded=%q", result, err, uploaded)
	}
	if result.Outputs["report"].(map[string]any)["path"] != "report.txt" {
		t.Fatal("declared output identity lost")
	}
}
