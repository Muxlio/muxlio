package accounting

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"testing"
)

func TestSummaryPreservesSelectedFidelityWithoutAddingAlternativeEstimate(t *testing.T) {
	a := &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Costs: []contract.CostMeasure{{Currency: "USD", Basis: "api-equivalent", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "0.02", Source: "provider", Fidelity: "provider-estimate"}, Estimated: &contract.Reading[contract.Amount]{Value: "0.03", Source: "muxlio-pricing", Fidelity: "derived-exact", Method: "rule", Version: "1"}}}}}}}
	s, err := SummarizeAccounting([]AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil {
		t.Fatal(err)
	}
	v := s.Costs[0].Value
	if v.KnownSubtotal == nil || *v.KnownSubtotal != "0.02" || v.EstimatedFallbackSubtotal != nil || v.Evidence == nil || len(v.Evidence.Collected) != 1 || v.Evidence.Collected[0] != "provider-estimate" || len(v.Evidence.Estimated) != 0 {
		t.Fatalf("fidelity or alternative selection lost: %+v", v)
	}
	if err = ValidateSummary(s); err != nil {
		t.Fatal(err)
	}
}
