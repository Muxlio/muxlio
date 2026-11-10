package state

import "math"

// UsageTotal preserves availability: nil is unavailable, a pointer to zero is
// reported zero. Unknown counts attempts without either measurement. Estimates
// are executor-supplied, never inferred from credits or context windows.
type UsageTotal struct {
	Observed  *float64 `json:"observed"`
	Estimated *float64 `json:"estimated"`
	Unknown   int      `json:"unknown"`
}

type UsageSummary struct {
	Tokens  UsageTotal `json:"tokens"`
	CostUSD UsageTotal `json:"costUsd"`
}

func addMeasurement(total **float64, value float64) {
	if *total == nil {
		*total = new(float64)
	}
	**total = math.Min(math.MaxFloat64, **total+value)
}

func SummarizeUsage(attempts []ExecutionAttempt) UsageSummary {
	var summary UsageSummary
	for _, a := range attempts {
		var estimatedTokens, cost, estimatedCost *float64
		if m := a.Measurements; m != nil && m.Validate() == nil {
			if m.EstimatedTokens != nil {
				n := float64(*m.EstimatedTokens)
				estimatedTokens = &n
			}
			cost, estimatedCost = m.CostUSD, m.EstimatedCostUSD
		}
		if u := a.Usage; u != nil && u.Complete() && u.Input >= 0 && u.Output >= 0 {
			// Cached is a subset of input, not additional tokens.
			addMeasurement(&summary.Tokens.Observed, float64(u.Input)+float64(u.Output))
		} else if estimatedTokens != nil {
			addMeasurement(&summary.Tokens.Estimated, *estimatedTokens)
		} else {
			summary.Tokens.Unknown++
		}
		if cost != nil {
			addMeasurement(&summary.CostUSD.Observed, *cost)
		} else if estimatedCost != nil {
			addMeasurement(&summary.CostUSD.Estimated, *estimatedCost)
		} else {
			summary.CostUSD.Unknown++
		}
	}
	return summary
}
