package accounting

import (
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func regressionCount(value string) contract.Measure[contract.Count] {
	return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: contract.Count(value), Source: "fixture", Fidelity: "metered"}}
}

func completeTokens(tokens contract.TokenMeasures) AttemptView {
	return AttemptView{State: "succeeded", Accounting: &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Tokens: &tokens}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}}}}
}

func TestCompleteCoverageRequiresUsableTotalsForEveryScope(t *testing.T) {
	full := completeTokens(contract.TokenMeasures{Total: regressionCount("100")})
	partial := completeTokens(contract.TokenMeasures{Input: regressionCount("80")})
	for _, views := range [][]AttemptView{{full, partial}, {completeTokens(contract.TokenMeasures{Total: regressionCount("100"), Input: regressionCount("80")})}} {
		// The second fixture does have a usable total; exercise an additional
		// incomplete disjoint scope within that same attempt.
		if len(views) == 1 {
			views[0].Accounting.Entries = append(views[0].Accounting.Entries, partial.Accounting.Entries[0])
			views[0].Accounting.Entries[1].ID = "second"
		}
		s, err := SummarizeAccounting(views)
		if err != nil || s.Tokens.EffectiveTotal != nil || s.Tokens.KnownSubtotal == nil || *s.Tokens.KnownSubtotal != "100" {
			t.Fatalf("incomplete scope presented as complete: %+v %v", s.Tokens, err)
		}
		if err := CheckLimit(views, Limit{Kind: "tokens", Maximum: "150"}, true); err == nil || !strings.Contains(err.Error(), "missing-accounting") {
			t.Fatalf("fail-closed admission accepted incomplete scope: %v", err)
		}
	}
	zero, err := SummarizeAccounting([]AttemptView{completeTokens(contract.TokenMeasures{Total: regressionCount("0")})})
	if err != nil || zero.Tokens.EffectiveTotal == nil || *zero.Tokens.EffectiveTotal != "0" {
		t.Fatalf("reported zero lost: %+v %v", zero.Tokens, err)
	}
}

func TestProxyTotalDoesNotHideCollectedDimensions(t *testing.T) {
	tokens := contract.TokenMeasures{Total: regressionCount("250"), Input: regressionCount("80"), Output: regressionCount("20")}
	tokens.Total.Collected.Fidelity = "proxy"
	s, err := SummarizeAccounting([]AttemptView{completeTokens(tokens)})
	if err != nil || s.Tokens.EffectiveTotal == nil || *s.Tokens.EffectiveTotal != "100" {
		t.Fatalf("proxy displaced eligible consumption: %+v %v", s.Tokens, err)
	}
}

func TestEconomicLimitsRespectExplicitInapplicability(t *testing.T) {
	for _, limit := range []Limit{{Kind: "credits", Provider: "service", Unit: "credit", Maximum: "10"}, {Kind: "cost", Currency: "USD", Basis: "api-equivalent", Maximum: "10"}} {
		for _, state := range []string{"running", "succeeded"} {
			v := AttemptView{State: state, Accounting: &contract.Accounting{Revision: 1, Final: state == "succeeded", Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: limit.Kind}, State: "inapplicable"}}}}
			if err := CheckLimit([]AttemptView{v}, limit, true); err != nil {
				t.Fatalf("%s %s inapplicable: %v", state, limit.Kind, err)
			}
			if err := CheckLimit([]AttemptView{v, {State: "succeeded"}}, limit, true); err == nil {
				t.Fatal("one inapplicable attempt concealed another unknown attempt")
			}
		}
	}
}

func TestFirstRecorderMutationAdvancesPublishedRevision(t *testing.T) {
	r := NewRecorder()
	before, _ := r.Snapshot()
	if err := r.Record(Part{Scope: "call", Entry: contract.AccountingEntry{ID: "call"}}); err != nil {
		t.Fatal(err)
	}
	after, _ := r.Snapshot()
	if after.Revision <= before.Revision {
		t.Fatal("first mutation reused a published revision")
	}
}

func TestModelReportsSurviveReplacementWithoutCreatingConsumption(t *testing.T) {
	r := NewRecorder()
	for _, m := range []contract.AccountingModel{{ID: "auto", Role: "selected", Source: "session-config"}, {ID: "model-1", Role: "served", Source: "terminal-usage"}} {
		if err := r.ReportModel(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ReplaceScope("call", nil); err != nil {
		t.Fatal(err)
	}
	a, err := r.Finish()
	if err != nil || len(a.Models) != 2 {
		t.Fatalf("models lost: %+v %v", a, err)
	}
	s, err := SummarizeAccounting([]AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil || s.Tokens.KnownSubtotal != nil {
		t.Fatal("model identity invented consumption")
	}
}
