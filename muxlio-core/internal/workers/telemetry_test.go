package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestScriptTelemetryReportsAdapterUsage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		usage  *contract.TokenUsage
	}{
		{name: "reported", output: `{"usage":{"input":12,"output":3,"cached":4}}`, usage: &contract.TokenUsage{Input: 12, Output: 3, Cached: 4}},
		{name: "zero", output: `{"usage":{"input":0,"output":0,"cached":0}}`, usage: &contract.TokenUsage{}},
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
			// Call directly: usage must be on the adapter span even without an
			// enclosing ExecuteAttempt or remote worker span.
			result, err := NewScriptExecutor(adapters, t.TempDir()).Execute(ExecuteRequest{Executor: "generic"})
			if err != nil || result.Error != "" {
				t.Fatalf("execute: %v %s", err, result.Error)
			}
			spans := exporter.GetSpans()
			if len(spans) != 1 || spans[0].Name != "execute_tool" {
				t.Fatalf("adapter spans: %v", spans)
			}
			want := map[string]int64{}
			if tc.usage != nil {
				want[telemetry.GenAIUsageInputTokens] = tc.usage.Input
				want[telemetry.GenAIUsageOutputTokens] = tc.usage.Output
				want[telemetry.GenAIUsageCacheReadTokens] = tc.usage.Cached
			}
			for _, attr := range spans[0].Attributes {
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
			if len(want) != 0 {
				t.Fatalf("missing reported usage: %v", want)
			}
		})
	}
}
