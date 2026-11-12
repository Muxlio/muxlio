package accounting

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"sort"
)

// ReferenceSummary is a valuation of consumed services, never an invoice.
// Methods disclose the economically different bases in a combined subtotal.
type ReferenceSummary struct {
	SummaryAmount
	Methods []string `json:"methods,omitempty"`
}

// referenceCost selects one amount for a decoder-certified disjoint entry.
// Multiple equally ranked amounts are ambiguous, even if numerically equal.
// Collected tool quotes (including zero) always precede local calculations.
func referenceCost(e contract.AccountingEntry) (*contract.CostMeasure, string) {
	var best *contract.CostMeasure
	bestRank, ambiguous, method := 5, false, ""
	for _, c := range e.Costs {
		if c.Currency != "USD" || c.Basis == "plan-debit" {
			continue
		}
		rank, m := 5, ""
		collected, estimated := selected(c.Amount)
		if collected != "" && (c.Basis == "unspecified" || c.Basis == "api-equivalent" || c.Basis == "provider-metered") {
			rank, m = 1, "provider-quote"
			if c.Basis == "provider-metered" {
				m = "provider-metered"
			}
		} else if collected != "" && c.Basis == "model-reference" {
			rank, m = 2, "provider-quote"
		} else if estimated != "" && c.Basis == "api-equivalent" {
			rank, m = 3, "published-token-rates"
		} else if estimated != "" && c.Basis == "credit-replacement" {
			rank, m = 4, "credit-replacement"
		}
		if rank == 5 {
			continue
		}
		if rank < bestRank {
			copy := c
			best, bestRank, ambiguous, method = &copy, rank, false, m
		} else if rank == bestRank {
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, ""
	}
	return best, method
}

// SummarizeAccounting retains all original meter partitions, and separately
// projects one selected USD valuation per disjoint scope. No rates are fetched
// or inferred by the reducer. Adapters establish ownership and coverage.
func SummarizeAccounting(views []AttemptView) (AccountingSummary, error) {
	out, err := summarizeAccounting(views)
	if err != nil {
		return out, err
	}
	projected := make([]AttemptView, 0, len(views))
	methods := map[string]bool{}
	key := contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: "unspecified"}
	for _, v := range views {
		p := AttemptView{State: v.State}
		a := &contract.Accounting{Revision: 1}
		state, known := "complete", false
		if v.Accounting != nil {
			a.Final = v.Accounting.Final
			for _, e := range v.Accounting.Entries {
				cost, method := referenceCost(e)
				if cost == nil {
					// Token-only entries can be descriptive breakdowns of an invocation
					// quote; adapter coverage, rather than entry counts, proves completeness.
					if len(e.Costs) > 0 {
						state = "partial"
					}
					continue
				}
				known = true
				methods[method] = true
				originalKey := contract.AccountingKey{Kind: "cost", Currency: cost.Currency, Basis: cost.Basis}
				if coverageFor(v, originalKey) != "complete" {
					state = "partial"
				}
				cost.Basis = key.Basis
				// Retain only the chosen reading; an alternative is not a contribution.
				if method == "provider-quote" || method == "provider-metered" {
					cost.Amount.Estimated = nil
				}
				a.Entries = append(a.Entries, contract.AccountingEntry{ID: e.ID, Costs: []contract.CostMeasure{*cost}})
			}
		}
		if !known {
			state = "unknown"
		}
		a.Coverage = []contract.MeterCoverage{{Key: key, State: state}}
		p.Accounting = a
		projected = append(projected, p)
	}
	subtotal, err := summarizeAccounting(projected)
	if err != nil {
		return out, err
	}
	value := finish(&meterAcc{}, projected, key)
	if len(subtotal.Costs) > 0 {
		value = subtotal.Costs[0].Value
	}
	out.ReferenceValue = &ReferenceSummary{SummaryAmount: value}
	for method := range methods {
		out.ReferenceValue.Methods = append(out.ReferenceValue.Methods, method)
	}
	sort.Strings(out.ReferenceValue.Methods)
	return out, nil
}
