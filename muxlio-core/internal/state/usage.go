package state

import (
	"math"
	"sort"

	"github.com/muxlio/muxlio-adapters/contract"
)

// UsageTotal preserves availability: nil is unavailable, a pointer to zero is
// reported zero. Unknown counts attempts without either measurement. Estimates
// are executor-supplied, never inferred from credits or context windows.
// Pending and Reporting apply to token run totals; other measurements omit them.
type UsageTotal struct {
	Observed  *float64 `json:"observed"`
	Estimated *float64 `json:"estimated"`
	Unknown   int      `json:"unknown"`
	Pending   int      `json:"pending,omitempty"`
	Reporting int      `json:"reporting,omitempty"`
}

// TokenDimensionTotal aggregates one reported token dimension across attempts.
// Observed nil means no attempt reported this dimension. Unknown counts finished
// attempts that did not report the dimension; pending attempts are excluded.
type TokenDimensionTotal struct {
	Observed  *int64 `json:"observed"`
	Reporting int    `json:"reporting"`
	Unknown   int    `json:"unknown"`
}

// TokenCoverage describes attempt populations for token reporting.
type TokenCoverage struct {
	Attempts  int `json:"attempts"`
	Pending   int `json:"pending"`
	Reporting int `json:"reporting"`
}

// TokenDimensionTotals exposes additive per-dimension subtotals without treating
// incomplete dimensions as a complete run total.
type TokenDimensionTotals struct {
	Input    TokenDimensionTotal `json:"input"`
	Output   TokenDimensionTotal `json:"output"`
	Cached   TokenDimensionTotal `json:"cached"`
	Coverage TokenCoverage       `json:"coverage"`
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
	Tokens           UsageTotal           `json:"tokens"`
	TokenDimensions  TokenDimensionTotals `json:"tokenDimensions"`
	CostUSD          UsageTotal           `json:"costUsd"`
	Credits          []CreditUsageTotal   `json:"credits,omitempty"`
}

func addMeasurement(total **float64, value float64) {
	if *total == nil {
		*total = new(float64)
	}
	**total = math.Min(math.MaxFloat64, **total+value)
}

func addInt64Measurement(total **int64, value int64) {
	if *total == nil {
		*total = new(int64)
		**total = value
		return
	}
	**total += value
}

func attemptPending(state string) bool {
	return state == "running" || state == "queued"
}

func summarizeTokenDimensions(attempts []ExecutionAttempt) TokenDimensionTotals {
	var dims TokenDimensionTotals
	dims.Coverage.Attempts = len(attempts)
	for _, a := range attempts {
		if attemptPending(a.State) {
			dims.Coverage.Pending++
			continue
		}
		usage := a.Usage
		reportedAny := usage != nil && usage.AnyReported()
		if reportedAny {
			dims.Coverage.Reporting++
		}
		for _, dim := range []struct {
			total       *TokenDimensionTotal
			reported    func(contract.TokenUsage) (int64, bool)
		}{
			{&dims.Input, contract.TokenUsage.ReportedInput},
			{&dims.Output, contract.TokenUsage.ReportedOutput},
			{&dims.Cached, contract.TokenUsage.ReportedCached},
		} {
			if usage == nil {
				dim.total.Unknown++
				continue
			}
			if v, ok := dim.reported(*usage); ok {
				dim.total.Reporting++
				addInt64Measurement(&dim.total.Observed, v)
			} else {
				dim.total.Unknown++
			}
		}
	}
	return dims
}

func SummarizeUsage(attempts []ExecutionAttempt) UsageSummary {
	var summary UsageSummary
	summary.TokenDimensions = summarizeTokenDimensions(attempts)
	summary.Tokens.Pending = summary.TokenDimensions.Coverage.Pending
	summary.Tokens.Reporting = summary.TokenDimensions.Coverage.Reporting
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
		tokenPending := attemptPending(a.State)
		if u := a.Usage; u != nil && u.Complete() && u.Input >= 0 && u.Output >= 0 {
			// Cached is a subset of input, not additional tokens.
			addMeasurement(&summary.Tokens.Observed, float64(u.Input)+float64(u.Output))
		} else if estimatedTokens != nil {
			addMeasurement(&summary.Tokens.Estimated, *estimatedTokens)
		} else if tokenPending {
			// In-flight attempts are neither unknown nor reporting yet.
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
