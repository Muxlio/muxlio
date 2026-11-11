package telemetry

import (
	"context"
	"strconv"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func collectedTokenReading(m contract.Measure[contract.Count]) bool {
	return m.Collected != nil && (m.Collected.Fidelity == "metered" || m.Collected.Fidelity == "derived-exact")
}

// UsageMetrics is called once at the executing worker or local executor, never
// again at a queued coordinator. Measurements represent disjoint invocation
// consumption, not a count or distribution of hidden model calls.
func UsageMetrics(ctx context.Context, a *contract.Accounting) {
	if a == nil || !a.Final || a.Validate() != nil {
		return
	}
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil {
		return
	}
	provider := ""
	for _, e := range a.Entries {
		if e.Tokens == nil {
			continue
		}
		if e.Provider == "" || (provider != "" && provider != e.Provider) {
			provider = "unknown"
			break
		}
		provider = e.Provider
	}
	if provider == "" {
		provider = "unknown"
	}
	attrs := []attribute.KeyValue{attribute.String(GenAIOperationName, "invoke_runner"), attribute.String("gen_ai.provider.name", provider)}
	meter := otel.Meter(scope)
	dimensions := []struct {
		name, instrument string
		value            accounting.SummaryAmount
		measure          func(*contract.TokenMeasures) contract.Measure[contract.Count]
	}{
		{"input", "input_tokens", s.TokenDimensions.Input, func(t *contract.TokenMeasures) contract.Measure[contract.Count] { return t.Input }},
		{"output", "output_tokens", s.TokenDimensions.Output, func(t *contract.TokenMeasures) contract.Measure[contract.Count] { return t.Output }},
		{"cacheRead", "cache_read.input_tokens", s.TokenDimensions.CacheRead, func(t *contract.TokenMeasures) contract.Measure[contract.Count] { return t.CacheRead }},
		{"cacheWrite", "cache_write.input_tokens", s.TokenDimensions.CacheWrite, func(t *contract.TokenMeasures) contract.Measure[contract.Count] { return t.CacheWrite }},
		{"reasoning", "reasoning.output_tokens", s.TokenDimensions.Reasoning, func(t *contract.TokenMeasures) contract.Measure[contract.Count] { return t.Reasoning }},
	}
	for _, dim := range dimensions {
		eligible := s.Tokens.Completeness == "complete" && dim.value.EffectiveTotal != nil && dim.value.EstimatedFallbackSubtotal == nil
		for _, e := range a.Entries {
			if e.Tokens != nil && !collectedTokenReading(dim.measure(e.Tokens)) {
				eligible = false
				break
			}
		}
		if eligible {
			n, err := strconv.ParseInt(*dim.value.EffectiveTotal, 10, 64)
			// Usage counters require positive contributions. Reported zero is
			// retained by the canonical snapshot and span attributes.
			if err == nil && n > 0 {
				counter, err := meter.Int64Counter("gen_ai.client.inference.usage."+dim.instrument, metric.WithUnit("{token}"))
				if err == nil {
					labels := append([]attribute.KeyValue(nil), attrs...)
					if dim.name != "reasoning" {
						labels = append(labels, attribute.String("gen_ai.token.modality", "unknown"))
					}
					counter.Add(ctx, n, metric.WithAttributes(labels...))
				}
			}
		}
		if dim.value.EstimatedFallbackSubtotal != nil {
			n, err := strconv.ParseInt(*dim.value.EstimatedFallbackSubtotal, 10, 64)
			if err == nil {
				counter, err := meter.Int64Counter("muxlio.accounting.estimated.tokens", metric.WithUnit("{token}"))
				if err == nil {
					counter.Add(ctx, n, metric.WithAttributes(attribute.String("dimension", dim.name), attribute.String("completeness", dim.value.Completeness)))
				}
			}
		}
	}
	// Economic diagnostics are approximate metric projections. Exact decimal
	// strings and provenance remain on the persisted accounting snapshot.
	for _, c := range s.Costs {
		estimatedEconomicMetric(ctx, "muxlio.accounting.estimated.cost", c.Value, attribute.String("currency", c.Currency), attribute.String("basis", c.Basis))
	}
	for _, c := range s.Credits {
		estimatedEconomicMetric(ctx, "muxlio.accounting.estimated.credits", c.Value, attribute.String("provider", c.Provider), attribute.String("unit", c.Unit))
	}
}

func estimatedEconomicMetric(ctx context.Context, name string, value accounting.SummaryAmount, attrs ...attribute.KeyValue) {
	if value.EstimatedFallbackSubtotal == nil {
		return
	}
	n, err := strconv.ParseFloat(*value.EstimatedFallbackSubtotal, 64)
	if err != nil {
		return
	}
	hist, err := otel.Meter(scope).Float64Histogram(name)
	if err == nil {
		hist.Record(ctx, n, metric.WithAttributes(append(attrs, attribute.String("completeness", value.Completeness))...))
	}
}
