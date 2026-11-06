package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracePropagationAndReportedUsage(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(old)
	defer provider.Shutdown(context.Background())
	ctx, end := Start(context.Background(), "run", RunAttributes("run", "workflow")...)
	member, _ := baggage.NewMember("secret", "must-not-export")
	bag, _ := baggage.New(member)
	carrier := Inject(baggage.ContextWithBaggage(ctx, bag))
	if len(carrier) != 1 || carrier["traceparent"] == "" {
		t.Fatalf("trace-only carrier: %v", carrier)
	}
	child, finish := Start(Extract(context.Background(), carrier), "attempt")
	Usage(child, &contract.TokenUsage{})
	finish(true)
	_, absent := Start(ctx, "absent")
	Usage(ctx, nil)
	absent(false)
	end(false)
	spans := exporter.GetSpans()
	if spans[0].Parent.SpanID() != spans[2].SpanContext.SpanID() || spans[0].SpanContext.TraceID() != spans[2].SpanContext.TraceID() {
		t.Fatal("lost remote parent")
	}
	attrs := map[attribute.Key]attribute.Value{}
	for _, a := range spans[0].Attributes {
		attrs[a.Key] = a.Value
	}
	for _, key := range []attribute.Key{GenAIUsageInputTokens, GenAIUsageOutputTokens, GenAIUsageCacheReadTokens} {
		value, ok := attrs[key]
		if !ok || value.AsInt64() != 0 {
			t.Fatalf("reported zero missing: %s", key)
		}
	}
	if len(spans[1].Attributes) != 0 {
		t.Fatalf("missing usage became zero: %v", spans[1].Attributes)
	}
	if spans[0].Status.Description != "" || len(spans[0].Events) != 0 {
		t.Fatal("error content exported")
	}
}

func TestSeparateDurationsAndSlots(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	defer otel.SetMeterProvider(old)
	defer provider.Shutdown(context.Background())
	ctx := context.Background()
	Duration(ctx, "muxlio.queue.delay", 2*time.Second, "generic")
	Duration(ctx, "muxlio.execution.duration", 3*time.Second, "generic")
	Duration(ctx, "muxlio.adapter.duration", time.Second, "generic")
	LeaseAge(ctx, "worker", time.Second)
	Slots(ctx, "worker", 2, 4)
	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	sums := map[string]float64{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch v := m.Data.(type) {
			case metricdata.Histogram[float64]:
				sums[m.Name] = v.DataPoints[0].Sum
				for _, a := range v.DataPoints[0].Attributes.ToSlice() {
					if string(a.Key) != AttrExecutor && (m.Name != "muxlio.lease.age" || string(a.Key) != AttrWorkerID) {
						t.Fatalf("unbounded metric dimension: %v", a)
					}
				}
			case metricdata.Gauge[float64]:
				sums[m.Name] = v.DataPoints[0].Value
			case metricdata.Gauge[int64]:
				sums[m.Name] = float64(v.DataPoints[0].Value)
			}
		}
	}
	for name, want := range map[string]float64{"muxlio.queue.delay": 2, "muxlio.execution.duration": 3, "muxlio.adapter.duration": 1, "muxlio.lease.age": 1, "muxlio.worker.slots.used": 2, "muxlio.worker.slots.capacity": 4, "muxlio.worker.slot_pressure": 0.5} {
		if got := sums[name]; got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}

func TestSetupDisabledAndInvalidExporter(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	t.Setenv("OTEL_TRACES_EXPORTER", "unsupported")
	shutdown, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_SDK_DISABLED", "false")
	if _, err := Setup(context.Background(), "test"); err == nil {
		t.Fatal("unsupported exporter accepted")
	}
}
