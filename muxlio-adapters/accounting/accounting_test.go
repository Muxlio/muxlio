package accounting

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"strings"
	"testing"
)

func TestAccountingLimitsUseExactThresholdAndEvidence(t *testing.T) {
	key := contract.AccountingKey{Kind: "credits", Provider: "service", Unit: "credit"}
	a := &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Credits: []contract.CreditMeasure{{Provider: "service", Unit: "credit", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "0.18065595854063016", Source: "provider", Fidelity: "metered"}}}}}}, Coverage: []contract.MeterCoverage{{Key: key, State: "complete"}}}
	views := []AttemptView{{State: "succeeded", Accounting: a}}
	limit := Limit{Kind: "credits", Provider: "service", Unit: "credit", Maximum: "0.18065595854063016"}
	if err := CheckLimit(views, limit, false); err != nil {
		t.Fatalf("exact terminal threshold: %v", err)
	}
	if err := CheckLimit(views, limit, true); err == nil || !strings.Contains(err.Error(), "budget-exhausted") {
		t.Fatalf("admission threshold: %v", err)
	}
	limit.Maximum = "0.18065595854063017"
	if err := CheckLimit(views, limit, true); err != nil {
		t.Fatal(err)
	}
	reading := a.Entries[0].Credits[0].Amount.Collected
	a.Entries[0].Credits[0].Amount = contract.Measure[contract.Amount]{Estimated: &contract.Reading[contract.Amount]{Value: reading.Value, Source: "estimator", Fidelity: "derived-exact", Method: "fixture", Version: "1"}}
	if err := CheckLimit(views, limit, true); err == nil || !strings.Contains(err.Error(), "missing-accounting") {
		t.Fatalf("collected-only accepted estimate: %v", err)
	}
	limit.Evidence = "collected-or-estimated"
	if err := CheckLimit(views, limit, true); err != nil {
		t.Fatal(err)
	}
	limit.Fidelity = "metered"
	if err := CheckLimit(views, limit, true); err == nil {
		t.Fatal("metered policy accepted local estimate")
	}
}

func TestExactAmountsAndOverflow(t *testing.T) {
	got, err := AddAmount("0.18065595854063016", "0.1")
	if err != nil || got != "0.28065595854063016" {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := AddCount("9223372036854775807", "1"); err == nil {
		t.Fatal("overflow accepted")
	}
}
func TestRecorderReplayAndReplacement(t *testing.T) {
	r := NewRecorder()
	p := Part{Scope: "turn", Entry: contract.AccountingEntry{ID: "call-1"}}
	if err := r.Record(p); err != nil {
		t.Fatal(err)
	}
	first, _ := r.Snapshot()
	if err := r.Record(p); err != nil {
		t.Fatal(err)
	}
	next, _ := r.Snapshot()
	if first.Revision != next.Revision {
		t.Fatal("duplicate advanced revision")
	}
	if err := r.ReplaceScope("turn", []Part{{Scope: "turn", Entry: contract.AccountingEntry{ID: "terminal"}}}); err != nil {
		t.Fatal(err)
	}
	end, err := r.Finish()
	if err != nil || !end.Final || len(end.Entries) != 1 || !strings.HasPrefix(end.Entries[0].ID, "bucket-") {
		t.Fatalf("%+v %v", end, err)
	}
}
func TestAlternativeReadingsAreNotAdded(t *testing.T) {
	a := &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Total: contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: "100", Source: "provider", Fidelity: "metered"}, Estimated: &contract.Reading[contract.Count]{Value: "120", Source: "estimator", Fidelity: "derived-exact", Method: "tokenizer", Version: "1"}}}}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}}}
	s, err := SummarizeAccounting([]AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil || s.Tokens.EffectiveTotal == nil || *s.Tokens.EffectiveTotal != "100" || s.Tokens.EstimatedFallbackSubtotal != nil {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestSummaryUnknownMetersAndTotalOnlyDimensions(t *testing.T) {
	m := func(v string) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: contract.Count(v), Source: "fixture", Fidelity: "metered"}}
	}
	a := &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Total: m("0")}}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}}}
	s, err := SummarizeAccounting([]AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Tokens.EffectiveTotal == nil || *s.Tokens.EffectiveTotal != "0" || s.TokenDimensions.Input.EffectiveTotal != nil || s.TokenDimensions.Input.Completeness != "unknown" {
		t.Fatalf("total-only coverage=%+v", s)
	}
	if s.Unattributed.Credits.UnknownAttempts != 1 || s.Unattributed.Costs.UnknownAttempts != 1 || s.Credits == nil || s.Costs == nil {
		t.Fatalf("undeclared meters=%+v", s)
	}
	a.Entries[0].Tokens = &contract.TokenMeasures{Total: m("99"), Input: m("10"), Output: m("5")}
	s, err = SummarizeAccounting([]AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Tokens.KnownSubtotal != nil || s.Tokens.EffectiveTotal != nil || s.Tokens.Completeness != "partial" || *s.TokenDimensions.Input.KnownSubtotal != "10" {
		t.Fatalf("conflicting total silently selected: %+v", s)
	}
}
