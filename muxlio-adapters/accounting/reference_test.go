package accounting

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"testing"
)

func TestReferenceSelectionAndIncompleteWorkflow(t *testing.T) {
	quote := contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "0", Source: "provider", Fidelity: "provider-estimate"}, Estimated: &contract.Reading[contract.Amount]{Value: "99", Source: "local", Fidelity: "derived-exact", Method: "tariff", Version: "1"}}
	credits := contract.Measure[contract.Amount]{Estimated: &contract.Reading[contract.Amount]{Value: "3.42", Source: "local", Fidelity: "derived-exact", Method: "credit-replacement", Version: "1"}}
	view := func(basis string, m contract.Measure[contract.Amount]) AttemptView {
		return AttemptView{State: "succeeded", Accounting: &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "invocation", Costs: []contract.CostMeasure{{Currency: "USD", Basis: basis, Amount: m}}}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: basis}, State: "complete"}}}}
	}
	views := []AttemptView{view("api-equivalent", quote), view("credit-replacement", credits), {State: "succeeded"}}
	s, err := SummarizeAccounting(views)
	if err != nil {
		t.Fatal(err)
	}
	v := s.ReferenceValue
	if v == nil || v.KnownSubtotal == nil || *v.KnownSubtotal != "3.42" || v.EffectiveTotal != nil || v.Attempts.Complete != 2 || v.Attempts.Unknown != 1 || len(v.Methods) != 2 || v.EstimatedFallbackSubtotal == nil || *v.EstimatedFallbackSubtotal != "3.42" {
		t.Fatalf("incorrect selection or coverage: %+v", v)
	}
	if err := ValidateSummary(s); err != nil {
		t.Fatal(err)
	}
	if quote.Estimated.Value != "99" {
		t.Fatal("projection mutated original evidence")
	}
	for _, views := range [][]AttemptView{nil, {{State: "succeeded"}}} {
		s, err = SummarizeAccounting(views)
		if err != nil || s.ReferenceValue.KnownSubtotal != nil {
			t.Fatalf("missing became zero: %+v %v", s.ReferenceValue, err)
		}
	}
}

func TestReferenceAmbiguityAndCurrencyDoNotInventValue(t *testing.T) {
	reading := contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "2", Source: "provider", Fidelity: "metered"}}
	entry := contract.AccountingEntry{ID: "call", Costs: []contract.CostMeasure{{Currency: "EUR", Basis: "unspecified", Amount: reading}, {Currency: "USD", Basis: "plan-debit", Amount: reading}}}
	if c, _ := referenceCost(entry); c != nil {
		t.Fatal("non-USD or plan debit promoted to reference")
	}
	entry.Costs = []contract.CostMeasure{{Currency: "USD", Basis: "unspecified", Amount: reading}, {Currency: "USD", Basis: "api-equivalent", Amount: reading}}
	if c, _ := referenceCost(entry); c != nil {
		t.Fatal("ambiguous quotes selected")
	}
	entry.Costs[1].Amount = contract.Measure[contract.Amount]{Estimated: reading.Collected}
	if c, m := referenceCost(entry); c == nil || m != "provider-quote" || c.Basis != "unspecified" {
		t.Fatal("direct quote did not win")
	}
}

func TestRecordPricedDoesNotRequireTariffWhenToolReportsUSD(t *testing.T) {
	r := NewRecorder()
	part := Part{Scope: "call", Entry: contract.AccountingEntry{ID: "call", Costs: []contract.CostMeasure{{Currency: "USD", Basis: "unspecified", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "0", Source: "tool", Fidelity: "metered"}}}}}}
	result, err := r.RecordPriced(part, Catalog{}, PriceRequest{})
	if err != nil || result.Amount != "0" {
		t.Fatalf("direct zero required an estimate: %+v %v", result, err)
	}
}
