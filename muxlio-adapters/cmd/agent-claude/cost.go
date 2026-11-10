package main

import (
	"encoding/json"
	"math"

	"github.com/muxlio/muxlio-adapters/contract"
)

// parseClaudeEstimatedCostUSD maps terminal Claude Code stream-json result fields
// into client-side spend estimates. total_cost_usd and totalCostUsd are SDK
// pricing-table estimates for the invocation, not authoritative billing.
// Per-model modelUsage breakdowns are not summed when the terminal total is
// absent — they can overlap the cumulative total when both are present.
func parseClaudeEstimatedCostUSD(ev map[string]json.RawMessage) *float64 {
	for _, key := range []string{"total_cost_usd", "totalCostUsd", "totalCostUSD"} {
		if raw, ok := ev[key]; ok {
			if value := parseNonNegativeUSD(raw); value != nil {
				return value
			}
		}
	}
	return nil
}

func withEstimatedCost(measurements *contract.Measurements, estimated *float64) *contract.Measurements {
	if estimated == nil {
		return measurements
	}
	if measurements == nil {
		return &contract.Measurements{EstimatedCostUSD: estimated}
	}
	measurements.EstimatedCostUSD = estimated
	return measurements
}

func parseNonNegativeUSD(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil
	}
	return &value
}
