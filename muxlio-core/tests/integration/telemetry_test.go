package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type telemetryTransport struct{ handler http.Handler }

func (r telemetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, req)
	return response.Result(), nil
}

type telemetryExecutor struct{ seen chan<- workers.ExecuteRequest }

func (e telemetryExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	e.seen <- req
	time.Sleep(5 * time.Millisecond)
	return workers.ExecuteResult{Logs: "private-transcript", Outputs: map[string]any{"private": "artifact-body"}}, nil
}

func TestTelemetryAcrossCoordinatorAndWorkers(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	reader := sdkmetric.NewManualReader()
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	oldTrace, oldMetric := otel.GetTracerProvider(), otel.GetMeterProvider()
	otel.SetTracerProvider(traces)
	otel.SetMeterProvider(metrics)
	defer func() {
		otel.SetTracerProvider(oldTrace)
		otel.SetMeterProvider(oldMetric)
		_ = traces.Shutdown(context.Background())
		_ = metrics.Shutdown(context.Background())
	}()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "telemetry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	coordinator := api.NewCoordinator(store)
	p := &plan.Plan{IRVersion: "1.0", Name: "telemetry", Root: "root", Subgraphs: map[string]plan.Subgraph{
		"root": {Nodes: []plan.Node{{ID: "outer", Kind: plan.KindMap, BodyRef: "steps", Items: &plan.Expr{Op: "input"}, Key: &plan.Expr{Op: "item"}}}},
		"steps": {Nodes: []plan.Node{
			{ID: "first", Kind: plan.KindExecute, Executor: "generic/first", Task: "private-prompt", Params: json.RawMessage(`{"credential":"secret-value"}`)},
			{ID: "second", Kind: plan.KindExecute, Executor: "generic/second", Task: "private-prompt"},
		}},
	}}
	engine := plan.NewEngineWithStore(p, coordinator, store)
	runID, err := engine.CreateRun(json.RawMessage(`["sensitive-map-key-input-body"]`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var tasks sync.WaitGroup
	defer func() { cancel(); tasks.Wait() }()
	engine.Context = ctx
	finished := make(chan error, 1)
	tasks.Add(1)
	go func() { defer tasks.Done(); finished <- engine.Run(runID) }()
	// Wait for the first attempt before leaving a queue interval with no workers.
	for {
		attempts, err := store.ListExecutionAttempts(runID, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(80 * time.Millisecond)
	client := &http.Client{Transport: telemetryTransport{api.NewWorkerProtocolHandler(coordinator)}}
	seen := make(chan workers.ExecuteRequest, 2)
	workerDone := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		tasks.Add(1)
		go func(name string) {
			defer tasks.Done()
			workerDone <- workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{
				CoordinatorURL: "http://coordinator", WorkerID: name, Capacity: 1,
				Capabilities: []workers.RemoteCapability{{Name: "generic/" + name}},
				HTTPClient:   client, PollInterval: time.Millisecond, HeartbeatInterval: 5 * time.Millisecond,
			}, telemetryExecutor{seen})
		}(name)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	for range 2 {
		if err := <-workerDone; err != nil {
			t.Fatal(err)
		}
	}
	var traceID trace.TraceID
	var runSpanID trace.SpanID
	workersSeen := map[string]bool{}
	spanByID := map[trace.SpanID]tracetest.SpanStub{}
	for _, s := range spans.GetSpans() {
		spanByID[s.SpanContext.SpanID()] = s
		if s.Name == "invoke_workflow" {
			traceID = s.SpanContext.TraceID()
			runSpanID = s.SpanContext.SpanID()
		}
	}
	if !traceID.IsValid() {
		t.Fatal("missing run span")
	}
	counts := map[string]int{}
	activationIDs := map[string]bool{}
	attemptIDs := map[string]bool{}
	for _, s := range spans.GetSpans() {
		counts[s.Name]++
		if s.SpanContext.TraceID() != traceID {
			t.Fatalf("disconnected span: %s", s.Name)
		}
		if s.Name == "muxlio.worker.attempt" {
			parent := spanByID[s.Parent.SpanID()]
			activation := spanByID[parent.Parent.SpanID()]
			outer := spanByID[activation.Parent.SpanID()]
			if parent.Name != "muxlio.attempt" || activation.Name != "muxlio.activation" || outer.Name != "muxlio.activation" || outer.Parent.SpanID() != runSpanID {
				t.Fatalf("broken ancestry: %s -> %s -> %s -> %s", s.Name, parent.Name, activation.Name, outer.Name)
			}
			for _, a := range parent.Attributes {
				if string(a.Key) == "muxlio.attempt.id" {
					attemptIDs[a.Value.AsString()] = true
				}
			}
			for _, a := range activation.Attributes {
				if string(a.Key) == "muxlio.activation.id" {
					activationIDs[a.Value.AsString()] = true
				}
			}
			for _, a := range s.Attributes {
				if string(a.Key) == "muxlio.worker.id" {
					workersSeen[a.Value.AsString()] = true
				}
				if string(a.Key) == "muxlio.attempt.id" && (!attemptIDs[a.Value.AsString()] || !strings.HasPrefix(a.Value.AsString(), "sha256:")) {
					t.Fatal("worker and coordinator attempt correlation differ")
				}
				if string(a.Key) == "muxlio.activation.id" && (!activationIDs[a.Value.AsString()] || !strings.HasPrefix(a.Value.AsString(), "sha256:")) {
					t.Fatal("worker and coordinator activation correlation differ")
				}
			}
		}
		exported := fmt.Sprintf("%v %v %v", s.Attributes, s.Events, s.Status)
		for _, private := range []string{"private-prompt", "secret-value", "private-transcript", "artifact-body", "input-body", "credential"} {
			if strings.Contains(exported, private) {
				t.Fatalf("private content exported: %s", private)
			}
		}
	}
	if counts["muxlio.worker.attempt"] != 2 || !workersSeen["first"] || !workersSeen["second"] {
		t.Fatalf("worker spans: %v %v", counts, workersSeen)
	}
	for range 2 {
		req := <-seen
		if !strings.Contains(req.ActivationID, "sensitive-map-key-input-body") || !strings.Contains(req.AttemptID, "sensitive-map-key-input-body") {
			t.Fatal("fixture did not exercise input-derived correlation IDs")
		}
		if trace.SpanContextFromContext(req.Context).TraceID() != traceID || req.TraceContext["traceparent"] == "" {
			t.Fatal("executor lost propagated context")
		}
	}
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	queue, execution := 0.0, 0.0
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok {
				for _, point := range h.DataPoints {
					if m.Name == "muxlio.queue.delay" {
						queue += point.Sum
					}
					if m.Name == "muxlio.execution.duration" {
						execution += point.Sum
					}
				}
			}
		}
	}
	if queue < 0.05 || execution <= 0 || queue <= execution {
		t.Fatalf("queue=%v execution=%v; measures must be separate", queue, execution)
	}
}
