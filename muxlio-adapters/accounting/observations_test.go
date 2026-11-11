package accounting

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"testing"
)

func cumulativePart(input, output string) Part {
	m := func(v string) contract.Measure[contract.Count] {
		if v == "" {
			return contract.Measure[contract.Count]{}
		}
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: contract.Count(v), Source: "fixture", Fidelity: "metered"}}
	}
	return Part{Scope: "turn", Entry: contract.AccountingEntry{ID: "turn", Tokens: &contract.TokenMeasures{Input: m(input), Output: m(output)}}}
}

func TestCumulativeReplacementPreservesOmittedDimensionsAndRejectsRegression(t *testing.T) {
	r := NewRecorder()
	_ = r.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "partial", "source-coverage-unverified")
	for _, part := range []Part{cumulativePart("100", "50"), cumulativePart("180", "80"), cumulativePart("200", "")} {
		if err := r.ObserveCumulative(part); err != nil {
			t.Fatal(err)
		}
	}
	a, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if a.Entries[0].Tokens.Input.Collected.Value != "200" || a.Entries[0].Tokens.Output.Collected.Value != "80" {
		t.Fatal("partial report discarded confirmed dimensions")
	}
	if err := r.ObserveCumulative(cumulativePart("100", "50")); err == nil {
		t.Fatal("reordered cumulative report regressed confirmed usage")
	}
	a, err = r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if a.Entries[0].Tokens.Input.Collected.Value != "200" || a.Coverage[0].State == "complete" {
		t.Fatal("regression lost confirmed usage or claimed complete coverage")
	}
}

func TestCumulativeRejectsEstimateOnlyRegressionOfCollectedUsage(t *testing.T) {
	r := NewRecorder()
	collected := func(v string) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: contract.Count(v), Source: "fixture", Fidelity: "metered"}}
	}
	estimated := func(v string) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Estimated: &contract.Reading[contract.Count]{Value: contract.Count(v), Source: "fixture", Fidelity: "derived-exact", Method: "fixture", Version: "1"}}
	}
	if err := r.ObserveCumulative(Part{Scope: "turn", Entry: contract.AccountingEntry{ID: "turn", Tokens: &contract.TokenMeasures{Input: collected("100")}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.ObserveCumulative(Part{Scope: "turn", Entry: contract.AccountingEntry{ID: "turn", Tokens: &contract.TokenMeasures{Input: estimated("50")}}}); err == nil {
		t.Fatal("estimate-only report regressed confirmed metered usage")
	}
	a, err := r.Snapshot()
	if err != nil || a.Entries[0].Tokens.Input.Collected.Value != "100" {
		t.Fatal("confirmed metered usage lost after rejected regression")
	}
}

func TestAmbiguousCumulativeScopeCannotEraseDisjointCalls(t *testing.T) {
	r := NewRecorder()
	p := recordedPart("first", "model-a")
	if err := r.ObserveDelta(p); err != nil {
		t.Fatal(err)
	}
	p = recordedPart("second", "model-b")
	if err := r.ObserveDelta(p); err != nil {
		t.Fatal(err)
	}
	p.Entry.ID = "terminal"
	if err := r.ObserveCumulative(p); err == nil {
		t.Fatal("ambiguous cumulative scope accepted")
	}
	a, err := r.Finish()
	if err != nil || len(a.Entries) != 2 {
		t.Fatalf("disjoint consumption lost: %+v %v", a, err)
	}
}
