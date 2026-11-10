package main

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseClaudeEstimatedCostUSDFromTerminalTotal(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want *float64
	}{
		{`{"total_cost_usd":0.0812}`, f64ptr(0.0812)},
		{`{"totalCostUsd":1.5}`, f64ptr(1.5)},
		{`{"total_cost_usd":0}`, f64ptr(0)},
		{`{"total_cost_usd":null}`, nil},
		{`{"total_cost_usd":-1}`, nil},
		{`{"total_cost_usd":"bad"}`, nil},
		{`{"modelUsage":{"claude-sonnet-4-5":{"costUSD":0.12}}}`, nil},
		{`{"total_cost_usd":0.2,"modelUsage":{"claude-sonnet-4-5":{"costUSD":0.12}}}`, f64ptr(0.2)},
		{`{"total_cost_usd":null,"totalCostUsd":0.3}`, f64ptr(0.3)},
		{`{"total_cost_usd":"invalid","totalCostUSD":0.4}`, f64ptr(0.4)},
	} {
		var ev map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.raw), &ev); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.raw, err)
		}
		got := parseClaudeEstimatedCostUSD(ev)
		if !costPtrEqual(got, tc.want) {
			t.Fatalf("parseClaudeEstimatedCostUSD(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestParseNonNegativeUSDRejectsNonFinite(t *testing.T) {
	for _, raw := range []string{`"NaN"`, `"Inf"`, `"-Inf"`} {
		if got := parseNonNegativeUSD(json.RawMessage(raw)); got != nil {
			t.Fatalf("parseNonNegativeUSD(%s) = %v, want nil", raw, got)
		}
	}
	if huge := parseNonNegativeUSD(json.RawMessage(`1e308`)); huge == nil || math.IsInf(*huge, 0) {
		t.Fatalf("finite large value rejected: %v", huge)
	}
}

func costPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func f64ptr(v float64) *float64 { return &v }
