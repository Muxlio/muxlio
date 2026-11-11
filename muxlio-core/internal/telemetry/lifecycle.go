package telemetry

import (
	"context"
	"github.com/muxlio/muxlio-adapters/accounting"
	"strconv"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const scope = "github.com/muxlio/muxlio-core"

// Inject carries only W3C trace identity. Baggage, headers and request content
// are deliberately excluded from durable assignments.
func Inject(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier
}

func Extract(ctx context.Context, carrier map[string]string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier(carrier))
}

func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(bool)) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, span := otel.Tracer(scope).Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func(failed bool) {
		if failed {
			// Errors can contain prompts, paths and credentials. Export a fixed class
			// instead of exception messages or arbitrary adapter output.
			span.SetAttributes(attribute.String("error.type", "_OTHER"))
			span.SetStatus(codes.Error, "")
		}
		span.End()
	}
}

// Duration uses bounded dimensions only; operation identity belongs on spans.
func Duration(ctx context.Context, name string, elapsed time.Duration, executor string) {
	if elapsed < 0 {
		elapsed = 0
	}
	h, err := otel.Meter(scope).Float64Histogram(name, metric.WithUnit("s"))
	if err == nil {
		h.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attribute.String(AttrExecutor, executor)))
	}
}

func Slots(ctx context.Context, workerID string, used, capacity int) {
	meter := otel.Meter(scope)
	attrs := metric.WithAttributes(attribute.String(AttrWorkerID, workerID))
	for name, value := range map[string]int{"muxlio.worker.slots.used": used, "muxlio.worker.slots.capacity": capacity} {
		g, err := meter.Int64Gauge(name, metric.WithUnit("{slot}"))
		if err == nil {
			g.Record(ctx, int64(value), attrs)
		}
	}
	pressure := 0.0
	if capacity > 0 {
		pressure = float64(used) / float64(capacity)
	} else if used > 0 {
		pressure = 1
	}
	g, err := meter.Float64Gauge("muxlio.worker.slot_pressure", metric.WithUnit("1"))
	if err == nil {
		g.Record(ctx, pressure, attrs)
	}
}

// Usage exports only counts explicitly reported by the executor contract.
// Missing usage stays absent, and an explicitly reported zero stays zero.
func Usage(ctx context.Context, usage *contract.Accounting) {
	if usage == nil || !usage.Final {
		return
	}
	if err := usage.Validate(); err != nil {
		return
	}
	summary, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: usage}})
	if err != nil || summary.Tokens.Completeness != "complete" {
		return
	}
	dims := []struct {
		key     attribute.Key
		measure accounting.SummaryAmount
		present func(*contract.TokenMeasures) bool
	}{
		{GenAIUsageInputTokens, summary.TokenDimensions.Input, func(t *contract.TokenMeasures) bool {
			return collectedTokenReading(t.Input)
		}},
		{GenAIUsageOutputTokens, summary.TokenDimensions.Output, func(t *contract.TokenMeasures) bool {
			return collectedTokenReading(t.Output)
		}},
		{GenAIUsageCacheReadTokens, summary.TokenDimensions.CacheRead, func(t *contract.TokenMeasures) bool {
			return collectedTokenReading(t.CacheRead)
		}},
	}
	for _, dim := range dims {
		if dim.measure.EffectiveTotal == nil || dim.measure.EstimatedFallbackSubtotal != nil {
			continue
		}
		complete := true
		for _, entry := range usage.Entries {
			if entry.Tokens != nil && !dim.present(entry.Tokens) {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		n, err := strconv.ParseInt(*dim.measure.EffectiveTotal, 10, 64)
		if err == nil {
			trace.SpanFromContext(ctx).SetAttributes(dim.key.Int64(n))
		}
	}
}

// LeaseAge measures time since claim or the last acknowledged renewal.
func LeaseAge(ctx context.Context, workerID string, age time.Duration) {
	if age < 0 {
		age = 0
	}
	h, err := otel.Meter(scope).Float64Histogram("muxlio.lease.age", metric.WithUnit("s"))
	if err == nil {
		h.Record(ctx, age.Seconds(), metric.WithAttributes(attribute.String(AttrWorkerID, workerID)))
	}
}
