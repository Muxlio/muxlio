package contract

import "testing"

func TestTokenSubsetsValidateEachComparableOrigin(t *testing.T) {
	for _, estimated := range []bool{false, true} {
		measure := func(n Count) Measure[Count] {
			r := &Reading[Count]{Value: n, Source: "fixture", Fidelity: "metered"}
			if estimated {
				r.Fidelity = "derived-exact"
				r.Method = "tokenizer"
				r.Version = "1"
				return Measure[Count]{Estimated: r}
			}
			return Measure[Count]{Collected: r}
		}
		for _, tokens := range []TokenMeasures{
			{Input: measure("100"), CacheRead: measure("150")},
			{Input: measure("100"), CacheWrite: measure("150")},
			{Input: measure("100"), CacheRead: measure("60"), CacheWrite: measure("60")},
			{Output: measure("10"), Reasoning: measure("11")},
		} {
			a := Accounting{Revision: 1, Entries: []AccountingEntry{{ID: "call", Tokens: &tokens}}}
			if a.Validate() == nil {
				t.Fatalf("estimated=%v: accepted invalid subsets: %+v", estimated, tokens)
			}
		}
	}
}
