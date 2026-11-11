package telemetry

import (
	"context"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestUsageMetricsSeparateCollectedEstimatesAndSubsets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	defer otel.SetMeterProvider(old)
	defer provider.Shutdown(context.Background())
	measure := func(n contract.Count) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: n, Source: "fixture", Fidelity: "metered"}}
	}
	makeSnapshot := func() *contract.Accounting {
		return &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Input: measure("100"), Output: measure("20"), CacheRead: measure("10"), CacheWrite: measure("5"), Reasoning: measure("4")}}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}}}
	}
	ctx := context.Background()
	UsageMetrics(ctx, makeSnapshot())
	for _, kind := range []string{"non-final", "partial", "proxy", "provider-estimate"} {
		a := makeSnapshot()
		switch kind {
		case "non-final":
			a.Final = false
		case "partial":
			a.Coverage[0].State = "partial"
		default:
			a.Entries[0].Tokens.Input.Collected.Fidelity = kind
			a.Entries[0].Tokens.Output.Collected.Fidelity = kind
			a.Entries[0].Tokens.CacheRead.Collected.Fidelity = kind
			a.Entries[0].Tokens.CacheWrite.Collected.Fidelity = kind
			a.Entries[0].Tokens.Reasoning.Collected.Fidelity = kind
		}
		UsageMetrics(ctx, a)
	}
	a := makeSnapshot()
	for _, m := range []*contract.Measure[contract.Count]{&a.Entries[0].Tokens.Input, &a.Entries[0].Tokens.Output, &a.Entries[0].Tokens.CacheRead, &a.Entries[0].Tokens.CacheWrite, &a.Entries[0].Tokens.Reasoning} {
		r := *m.Collected
		r.Method = "fixture"
		r.Version = "1"
		r.Fidelity = "derived-exact"
		m.Estimated = &r
		m.Collected = nil
	}
	a.Entries[0].Costs = []contract.CostMeasure{{Currency: "USD", Basis: "api-equivalent", Amount: contract.Measure[contract.Amount]{Estimated: &contract.Reading[contract.Amount]{Value: "0.02", Source: "fixture", Fidelity: "derived-exact", Method: "fixture", Version: "1"}}}}
	UsageMetrics(ctx, a)
	var result metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &result); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"input_tokens": 100, "output_tokens": 20, "cache_read.input_tokens": 10, "cache_write.input_tokens": 5, "reasoning.output_tokens": 4}
	seen := map[string]bool{}
	estimates := 0
	costs := 0
	for _, scope := range result.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "muxlio.accounting.estimated.tokens" {
				data, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("incorrect estimated instrument: %T", m.Data)
				}
				estimates = len(data.DataPoints)
				continue
			}
			if m.Name == "muxlio.accounting.estimated.cost" {
				data, ok := m.Data.(metricdata.Histogram[float64])
				if !ok || len(data.DataPoints) != 1 || data.DataPoints[0].Sum != 0.02 {
					t.Fatalf("economic diagnostic lost: %+v", m)
				}
				costs++
				continue
			}
			for suffix, value := range want {
				if m.Name != "gen_ai.client.inference.usage."+suffix {
					continue
				}
				seen[suffix] = true
				data, ok := m.Data.(metricdata.Sum[int64])
				if !ok || len(data.DataPoints) != 1 || data.DataPoints[0].Value != value {
					t.Fatalf("estimates or incomplete readings entered provider usage: %+v", m)
				}
				for _, attr := range data.DataPoints[0].Attributes.ToSlice() {
					switch string(attr.Key) {
					case GenAIOperationName, "gen_ai.provider.name", "gen_ai.token.modality":
					default:
						t.Fatalf("unbounded dimension: %s", attr.Key)
					}
				}
			}
		}
	}
	if len(seen) != len(want) || estimates != 5 || costs != 1 {
		t.Fatalf("missing metric families: collected=%v estimates=%d costs=%d", seen, estimates, costs)
	}
}
