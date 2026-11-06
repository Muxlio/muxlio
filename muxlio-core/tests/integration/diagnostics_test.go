package integration_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestRemoteMissingAdapterDiagnosticSurvivesPersistenceAndHTTP(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	coordinator := api.NewCoordinator(store)
	coordinator.Context = ctx
	server := httptest.NewServer(api.NewWorkerProtocolHandler(coordinator))
	defer server.Close()
	stopped := make(chan error, 1)
	executor := workers.NewScriptExecutor(t.TempDir(), t.TempDir())
	go func() {
		stopped <- workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{
			CoordinatorURL: server.URL, WorkerID: "worker", Capacity: 1,
			Capabilities: []workers.RemoteCapability{{Name: "generic/missing"}},
			PollInterval: time.Millisecond, HeartbeatInterval: 10 * time.Millisecond,
		}, executor)
	}()
	var p plan.Plan
	if err := json.Unmarshal([]byte(`{"irVersion":"1.0","name":"remote","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/missing"}]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	engine := plan.NewEngineWithStore(&p, coordinator, store)
	engine.Context = ctx
	runID, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(runID); err == nil {
		t.Fatal("missing adapter succeeded")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	attempts, err := store.ListExecutionAttempts(runID, "")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	d := attempts[0].Result.Diagnostic
	if d == nil || d.Code != "MUXLIO_ADAPTER_NOT_FOUND" || d.Source == nil || d.AttemptID == "" || len(d.Causes) == 0 {
		t.Fatalf("lost remote diagnostic: %+v", d)
	}
	response := httptest.NewRecorder()
	api.NewRouter(store, nil).ServeHTTP(response, httptest.NewRequest("GET", "/v1/runs/"+runID, nil))
	var body struct {
		Diagnostics []diagnostic.Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || len(body.Diagnostics) != 1 || body.Diagnostics[0].ID != d.ID {
		t.Fatalf("HTTP diagnostic: %s err=%v", response.Body.String(), err)
	}
	// Durable worker receipts carry the same stable code after transport.
	raw, err := store.WorkerResult(d.AttemptID)
	if err != nil || !strings.Contains(string(raw), "MUXLIO_ADAPTER_NOT_FOUND") {
		t.Fatalf("durable diagnostic: %s err=%v", raw, err)
	}
}
