package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type privateErrorExecutor struct{}

type accountingMetricExecutor struct{ snapshot *contract.Accounting }

func (e accountingMetricExecutor) Execute(ExecuteRequest) (ExecuteResult, error) {
	return ExecuteResult{Accounting: e.snapshot}, nil
}

type accountingMetricQueue struct{ inner Executor }

func (e accountingMetricQueue) Execute(req ExecuteRequest) (ExecuteResult, error) {
	return e.ExecuteQueued(req)
}
func (e accountingMetricQueue) ExecuteQueued(req ExecuteRequest) (ExecuteResult, error) {
	return ExecuteAttempt(e.inner, req)
}

func TestQueuedCoordinatorDoesNotCountWorkerUsageAgain(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	defer otel.SetMeterProvider(old)
	defer provider.Shutdown(context.Background())
	m := func(n contract.Count) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: n, Source: "fixture", Fidelity: "metered"}}
	}
	a := &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Input: m("100"), Output: m("20")}}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}}}
	if _, err := ExecuteAttempt(accountingMetricQueue{inner: accountingMetricExecutor{snapshot: a}}, ExecuteRequest{Context: context.Background(), Executor: "generic"}); err != nil {
		t.Fatal(err)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == "gen_ai.client.inference.usage.input_tokens" {
				sum := metric.Data.(metricdata.Sum[int64])
				if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 100 {
					t.Fatalf("queued boundary duplicated worker usage: %+v", sum)
				}
				return
			}
		}
	}
	t.Fatal("worker usage metric missing")
}

func (privateErrorExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	return ExecuteResult{Logs: "transcript-body"}, errors.New(req.Task + " " + req.Secrets["token"])
}

func TestAttemptTelemetryExcludesErrorAndRequestContent(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(old)
	defer provider.Shutdown(context.Background())
	_, err := ExecuteAttempt(privateErrorExecutor{}, ExecuteRequest{
		RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "generic",
		Task: "prompt-body", Secrets: map[string]string{"token": "secret-token"},
		Inputs: map[string]any{"private": "input-body"},
	})
	if err == nil {
		t.Fatal("expected executor error")
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans: %v", spans)
	}
	exported := fmt.Sprintf("%v %v %v", spans[0].Attributes, spans[0].Events, spans[0].Status)
	for _, private := range []string{"prompt-body", "secret-token", "input-body", "transcript-body"} {
		if strings.Contains(exported, private) {
			t.Fatalf("content exported: %s", private)
		}
	}
	if spans[0].Status.Code.String() != "Error" {
		t.Fatal("failure status missing")
	}
}

func TestScriptTelemetryMeasuresAdapterInvocation(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
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
	adapters := t.TempDir()
	if err := os.WriteFile(filepath.Join(adapters, "generic"), []byte("#!/bin/sh\ncat > request.json\necho '{\"logs\":\"private-transcript\"}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	executor := NewScriptExecutor(adapters, t.TempDir())
	member, _ := baggage.NewMember("private", "private-baggage")
	bag, _ := baggage.New(member)
	ctx := baggage.ContextWithBaggage(context.Background(), bag)
	scratch := t.TempDir()
	result, err := ExecuteAttempt(executor, ExecuteRequest{Context: ctx, RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "generic", Task: "private-prompt", attemptScratch: scratch})
	if err != nil || result.Error != "" {
		t.Fatalf("execute: %v %s", err, result.Error)
	}
	spans := exporter.GetSpans()
	if len(spans) != 2 || spans[0].Name != "execute_tool" || spans[0].Parent.SpanID() != spans[1].SpanContext.SpanID() {
		t.Fatalf("adapter ancestry: %v", spans)
	}
	raw, err := os.ReadFile(filepath.Join(scratch, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request contract.Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	wantParent := "00-" + spans[0].SpanContext.TraceID().String() + "-" + spans[0].SpanContext.SpanID().String() + "-01"
	if len(request.TraceContext) != 1 || request.TraceContext["traceparent"] != wantParent {
		t.Fatalf("adapter did not receive trace-only parent: %v", request.TraceContext)
	}
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok && len(h.DataPoints) == 1 && h.DataPoints[0].Count == 1 && h.DataPoints[0].Sum > 0 {
				found[m.Name] = true
			}
		}
	}
	if !found["muxlio.adapter.duration"] || !found["muxlio.execution.duration"] {
		t.Fatalf("missing invocation timing: %v", found)
	}
}

func TestScriptTelemetryPreservesExecutorCancellationContext(t *testing.T) {
	adapters := t.TempDir()
	if err := os.WriteFile(filepath.Join(adapters, "generic"), []byte("#!/bin/sh\ncat >/dev/null\necho '{}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	executor := NewScriptExecutor(adapters, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor.Context = ctx
	result, err := executor.Execute(ExecuteRequest{Executor: "generic"})
	if err != nil || !strings.Contains(result.Error, "cancelled") {
		t.Fatalf("executor context lost: %v, %s", err, result.Error)
	}
}

func TestRemoteWorkerAttemptTelemetryReportsAccounting(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(old)
	defer provider.Shutdown(context.Background())

	m := func(n contract.Count) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: n, Source: "fixture", Fidelity: "metered"}}
	}
	accounting := &contract.Accounting{
		Revision: 1, Final: true,
		Entries:  []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Input: m("9"), Output: m("2")}}},
		Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(r.URL.Path, "register"):
			_ = json.NewEncoder(w).Encode(remoteRegistrationResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(r.URL.Path, "heartbeat"):
			_ = json.NewEncoder(w).Encode(remoteHeartbeatResult{CoordinatorEpoch: 1})
		case strings.HasSuffix(r.URL.Path, "assignments") && !strings.HasSuffix(r.URL.Path, "complete"):
			_ = json.NewEncoder(w).Encode(remoteAssignment{AssignmentID: "a", RunID: "r", ActivationID: "act", AttemptID: "attempt", CoordinatorEpoch: 1, Generation: 1, Request: json.RawMessage(`{"executor":"test"}`)})
		case strings.HasSuffix(r.URL.Path, "complete"):
			cancel()
		default:
			w.WriteHeader(http.StatusNoContent)
		}
		return w.Result(), nil
	})}
	executor := accountingMetricExecutor{snapshot: accounting}
	if err := RunRemoteWorker(ctx, RemoteWorkerConfig{CoordinatorURL: "http://coordinator", WorkerID: "worker", HTTPClient: client, HeartbeatInterval: time.Second, WorkBase: t.TempDir()}, executor); err != nil {
		t.Fatal(err)
	}
	var workerSpan *tracetest.SpanStub
	want := map[string]int64{telemetry.GenAIUsageInputTokens: 9, telemetry.GenAIUsageOutputTokens: 2}
	for i := range exporter.GetSpans() {
		if exporter.GetSpans()[i].Name == "muxlio.worker.attempt" {
			workerSpan = &exporter.GetSpans()[i]
			break
		}
	}
	if workerSpan == nil {
		t.Fatal("worker attempt span missing")
	}
	for _, attr := range workerSpan.Attributes {
		key := string(attr.Key)
		if value, ok := want[key]; ok {
			if attr.Value.AsInt64() != value {
				t.Fatalf("unexpected %s=%d", key, attr.Value.AsInt64())
			}
			delete(want, key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing worker usage attributes: %v", want)
	}
}

func TestScriptTelemetryReportsAdapterUsage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		usage  *[3]int64
	}{
		{name: "reported", output: `{"accounting":{"revision":1,"final":true,"entries":[{"id":"call","tokens":{"input":{"collected":{"value":"12","source":"fixture","fidelity":"metered"}},"output":{"collected":{"value":"3","source":"fixture","fidelity":"metered"}},"cacheRead":{"collected":{"value":"4","source":"fixture","fidelity":"metered"}}}}],"coverage":[{"key":{"kind":"tokens"},"state":"complete"}]}}`, usage: &[3]int64{12, 3, 4}},
		{name: "zero", output: `{"accounting":{"revision":1,"final":true,"entries":[{"id":"call","tokens":{"input":{"collected":{"value":"0","source":"fixture","fidelity":"metered"}},"output":{"collected":{"value":"0","source":"fixture","fidelity":"metered"}},"cacheRead":{"collected":{"value":"0","source":"fixture","fidelity":"metered"}}}}],"coverage":[{"key":{"kind":"tokens"},"state":"complete"}]}}`, usage: &[3]int64{}},
		{name: "unavailable", output: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			old := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			defer otel.SetTracerProvider(old)
			defer provider.Shutdown(context.Background())
			adapters := t.TempDir()
			if err := os.WriteFile(filepath.Join(adapters, "generic"), []byte("#!/bin/sh\ncat >/dev/null\necho '"+tc.output+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			result, err := ExecuteAttempt(NewScriptExecutor(adapters, t.TempDir()), ExecuteRequest{Context: context.Background(), Executor: "generic"})
			if err != nil || result.Error != "" {
				t.Fatalf("execute: %v %s", err, result.Error)
			}
			if (result.Accounting == nil) != (tc.usage == nil) {
				t.Fatalf("script accounting availability changed: %+v", result.Accounting)
			}
			spans := exporter.GetSpans()
			if len(spans) != 2 {
				t.Fatalf("adapter spans: %v", spans)
			}
			want := map[string]int64{}
			if tc.usage != nil {
				want[telemetry.GenAIUsageInputTokens] = tc.usage[0]
				want[telemetry.GenAIUsageOutputTokens] = tc.usage[1]
				want[telemetry.GenAIUsageCacheReadTokens] = tc.usage[2]
			}
			for _, span := range spans {
				for _, attr := range span.Attributes {
					key := string(attr.Key)
					if !strings.HasPrefix(key, "gen_ai.usage.") {
						continue
					}
					value, ok := want[key]
					if !ok || attr.Value.AsInt64() != value {
						t.Fatalf("unexpected usage attribute: %v", attr)
					}
					delete(want, key)
				}
			}
			if len(want) != 0 {
				t.Fatalf("missing reported usage: %v", want)
			}
		})
	}
}
