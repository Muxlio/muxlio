package main

import (
	"encoding/json"
	"testing"
)

func TestClaudeCostIsExactCollectedProviderEstimate(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"total_cost_usd":0.0812}`, "0.0812"}, {`{"total_cost_usd":0}`, "0"},
		{`{"total_cost_usd":0.18065595854063016}`, "0.18065595854063016"},
		{`{"total_cost_usd":null}`, ""}, {`{"total_cost_usd":-1}`, ""},
		{`{"total_cost_usd":"bad"}`, ""}, {`{"total_cost_usd":1e308}`, ""},
		{`{"modelUsage":{"model":{"costUSD":0.12}}}`, ""},
		{`{"total_cost_usd":0.2,"modelUsage":{"model":{"costUSD":0.12}}}`, "0.2"},
	} {
		var ev map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.raw), &ev); err != nil {
			t.Fatal(err)
		}
		entry, _ := claudeAccountingEntry(ev)
		if tc.want == "" {
			if len(entry.Costs) != 0 {
				t.Fatalf("unexpected cost from %s", tc.raw)
			}
			continue
		}
		if len(entry.Costs) != 1 {
			t.Fatalf("missing cost from %s", tc.raw)
		}
		cost := entry.Costs[0]
		if cost.Amount.Collected == nil || string(cost.Amount.Collected.Value) != tc.want || cost.Amount.Collected.Fidelity != "provider-estimate" || cost.Amount.Estimated != nil || cost.Currency != "USD" || cost.Basis != "unspecified" {
			t.Fatalf("unexpected cost: %+v", cost)
		}
	}
}
