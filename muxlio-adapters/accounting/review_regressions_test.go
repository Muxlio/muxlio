package accounting

import (
	"fmt"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestInapplicabilityCannotOverrideReportedReadings(t *testing.T) {
	for _, kind := range []string{"tokens", "credits", "cost"} {
		for _, applicabilityOnly := range []bool{false, true} {
			for _, estimated := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/applicability=%v/estimated=%v", kind, applicabilityOnly, estimated), func(t *testing.T) {
					key := contract.AccountingKey{Kind: kind}
					entry := contract.AccountingEntry{ID: "call"}
					amount := contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "100", Source: "fixture", Fidelity: "metered"}}
					if estimated {
						amount.Estimated = &contract.Reading[contract.Amount]{Value: "100", Source: "fixture", Fidelity: "derived-exact", Method: "fixture", Version: "1"}
						amount.Collected = nil
					}
					switch kind {
					case "tokens":
						m := regressionCount("100")
						if estimated {
							m.Estimated = &contract.Reading[contract.Count]{Value: "100", Source: "fixture", Fidelity: "derived-exact", Method: "fixture", Version: "1"}
							m.Collected = nil
						}
						entry.Tokens = &contract.TokenMeasures{Total: m}
					case "credits":
						key.Provider, key.Unit = "service", "credit"
						entry.Credits = []contract.CreditMeasure{{Provider: key.Provider, Unit: key.Unit, Amount: amount}}
					case "cost":
						key.Currency, key.Basis = "USD", "api-equivalent"
						entry.Costs = []contract.CostMeasure{{Currency: key.Currency, Basis: key.Basis, Amount: amount}}
					}
					coverage := []contract.MeterCoverage{{Key: key, State: "inapplicable"}}
					v := AttemptView{State: "succeeded", Accounting: &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{entry}}}
					if applicabilityOnly {
						v.Applicability = coverage
					} else {
						v.Accounting.Coverage = coverage
					}
					for _, policy := range []string{"fail-open", "fail-closed"} {
						limit := Limit{Kind: kind, Maximum: "10", Provider: key.Provider, Unit: key.Unit, Currency: key.Currency, Basis: key.Basis, OnMissing: policy}
						if err := CheckLimit([]AttemptView{v}, limit, true); err == nil || !strings.Contains(err.Error(), "inconsistent-accounting") {
							t.Fatalf("contradiction bypassed budget: %v", err)
						}
					}
					s, err := SummarizeAccounting([]AttemptView{v})
					if err != nil {
						t.Fatal(err)
					}
					row := s.Tokens
					if kind == "credits" {
						row = s.Credits[0].Value
					}
					if kind == "cost" {
						row = s.Costs[0].Value
					}
					if row.Completeness != "partial" || row.KnownSubtotal == nil || *row.KnownSubtotal != "100" || row.EffectiveTotal != nil {
						t.Fatalf("contradictory coverage hid consumption: %+v", row)
					}
				})
			}
		}
	}
	// A genuinely inapplicable meter does not consume even a zero allowance.
	if err := CheckLimit([]AttemptView{{State: "succeeded", Applicability: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "inapplicable"}}}}, Limit{Kind: "tokens", Maximum: "0"}, true); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementIdentitiesSurviveRemovalAndEnforceCapacity(t *testing.T) {
	r, _ := NewRecorderWithLimits(RecorderLimits{MaxEvents: 2, MaxBuckets: 2, MaxSnapshotBytes: 2048})
	first, second := recordedPart("first", "model"), recordedPart("second", "model")
	for _, part := range []Part{first, second} {
		if err := r.ReplaceScope("stream", []Part{part}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := r.Snapshot()
	if err := r.ObserveDelta(first); err != nil {
		t.Fatal(err)
	}
	after, _ := r.Snapshot()
	if after.Revision != before.Revision || after.Entries[0].Tokens.Total.Collected.Value != "1" {
		t.Fatal("removed replacement counted again")
	}
	if err := r.ReplaceScope("stream", []Part{recordedPart("third", "model")}); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("replacement bypassed identity limit: %v", err)
	}
	after, _ = r.Snapshot()
	if after.Entries[0].Tokens.Total.Collected.Value != "1" {
		t.Fatal("rejected replacement changed consumption")
	}
	other := first
	other.Scope = "different"
	if err := r.ReplaceScope(other.Scope, []Part{other}); err == nil {
		t.Fatal("reused replacement identity in another scope")
	}
}

func TestRepeatedCumulativeIdentityDoesNotConsumeNewEventSlots(t *testing.T) {
	r, _ := NewRecorderWithLimits(RecorderLimits{MaxEvents: 1, MaxBuckets: 2, MaxSnapshotBytes: 2048})
	for i := 1; i <= 10; i++ {
		p := recordedPart("same", "model")
		p.Entry.Tokens.Total.Collected.Value = contract.Count(fmt.Sprint(i))
		if err := r.ObserveCumulative(p); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := r.Finish()
	if a.Entries[0].Tokens.Total.Collected.Value != "10" {
		t.Fatal("cumulative updates were added")
	}
}

func TestSummaryValidationRequiresContributionsAndConsistentCoverage(t *testing.T) {
	for _, state := range []string{"succeeded", "running", "failed"} {
		v := completeTokens(contract.TokenMeasures{Total: regressionCount("0")})
		v.State = state
		s, err := SummarizeAccounting([]AttemptView{v, {State: "failed"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateSummary(s); err != nil {
			t.Fatalf("reducer output rejected: %v", err)
		}
	}
	for _, mutate := range []func(*SummaryAmount){
		func(v *SummaryAmount) { v.CollectedSubtotal = nil },
		func(v *SummaryAmount) { v.CollectedSubtotal = nil; v.KnownSubtotal = nil; v.EffectiveTotal = nil },
		func(v *SummaryAmount) { v.KnownSubtotal = nil },
		func(v *SummaryAmount) { v.Attempts.Unknown = 1 },
		func(v *SummaryAmount) { v.Attempts.Pending = 1 },
		func(v *SummaryAmount) { v.Attempts.Complete = 0 },
		func(v *SummaryAmount) { v.EffectiveTotal = nil },
		func(v *SummaryAmount) {
			v.Completeness = "inapplicable"
			v.Attempts = SummaryAttemptCounts{Inapplicable: 1}
			v.EffectiveTotal = nil
		},
	} {
		s, _ := SummarizeAccounting([]AttemptView{completeTokens(contract.TokenMeasures{Total: regressionCount("100")})})
		mutate(&s.Tokens)
		if err := ValidateSummary(s); err == nil {
			t.Fatalf("invalid imported summary accepted: %+v", s.Tokens)
		}
	}
}
