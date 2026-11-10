package state

import (
	"math"
	"sort"

	"github.com/muxlio/muxlio-adapters/contract"
)

// UsageTotal preserves availability: nil is unavailable, a pointer to zero is
// reported zero. Unknown counts attempts without either measurement. Estimates
// are executor-supplied, never inferred from credits or context windows.
type UsageTotal struct {
	Observed  *float64 `json:"observed"`
	Estimated *float64 `json:"estimated"`
	Unknown   int      `json:"unknown"`
}

// CreditUsageTotal aggregates provider-reported credits for one unit identity.
// Observed nil means no attempts reported this unit; unknown counts attempts
// with no credit measurement at all.
type CreditUsageTotal struct {
	Unit     string   `json:"unit"`
	Observed *float64 `json:"observed"`
	Unknown  int      `json:"unknown"`
}

type creditAccumulator struct {
	total    CreditUsageTotal
	reported int
}

type UsageSummary struct {
	Tokens  UsageTotal         `json:"tokens"`
	CostUSD UsageTotal         `json:"costUsd"`
	Credits []CreditUsageTotal `json:"credits,omitempty"`
}

func addMeasurement(total **float64, value float64) {
	if *total == nil {
		*total = new(float64)
	}
	**total = math.Min(math.MaxFloat64, **total+value)
}

func SummarizeUsage(attempts []ExecutionAttempt) UsageSummary {
	var summary UsageSummary
	creditTotals := map[string]*creditAccumulator{}
	for _, a := range attempts {
		var estimatedTokens, cost, estimatedCost *float64
		var credit *contract.ProviderCredit
		if m := a.Measurements; m != nil && m.Validate() == nil {
			if m.EstimatedTokens != nil {
				n := float64(*m.EstimatedTokens)
				estimatedTokens = &n
			}
			cost, estimatedCost = m.CostUSD, m.EstimatedCostUSD
			credit = m.Credit
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
		if credit == nil {
			continue
		}
		unit := credit.Unit
		if unit == "" {
			continue
		}
		row, ok := creditTotals[unit]
		if !ok {
			row = &creditAccumulator{total: CreditUsageTotal{Unit: unit}}
			creditTotals[unit] = row
		}
		if credit.Amount == nil {
			continue
		}
		row.reported++
		addMeasurement(&row.total.Observed, *credit.Amount)
	}
	if len(creditTotals) > 0 {
		summary.Credits = make([]CreditUsageTotal, 0, len(creditTotals))
		for _, row := range creditTotals {
			row.total.Unknown = len(attempts) - row.reported
			summary.Credits = append(summary.Credits, row.total)
		}
		sort.Slice(summary.Credits, func(i, j int) bool {
			return summary.Credits[i].Unit < summary.Credits[j].Unit
		})
	}
	return summary
}
