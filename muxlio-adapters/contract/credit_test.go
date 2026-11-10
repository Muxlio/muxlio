package contract

import (
	"encoding/json"
	"testing"
)

func TestProviderCreditRoundTrip(t *testing.T) {
	zero, amount := 0.0, 0.18065595854063016
	for _, credit := range []*ProviderCredit{
		{Unit: "credit", Amount: &amount, Provenance: CreditProvenancePerTurn},
		{Unit: "credit", Amount: &zero},
	} {
		raw, err := json.Marshal(Measurements{Credit: credit})
		if err != nil {
			t.Fatal(err)
		}
		var m Measurements
		if err := json.Unmarshal(raw, &m); err != nil || m.Credit == nil || m.Credit.Amount == nil || *m.Credit.Amount != *credit.Amount {
			t.Fatalf("round trip %s -> %+v err=%v", raw, m.Credit, err)
		}
	}
}

func TestMergeProviderCreditsSemantics(t *testing.T) {
	one, two, three := 1.0, 2.0, 3.0
	merged, err := MergeProviderCredits(
		&ProviderCredit{Unit: "credit", Amount: &one, Provenance: CreditProvenancePerTurn},
		&ProviderCredit{Unit: "credit", Amount: &two, Provenance: CreditProvenancePerTurn},
	)
	if err != nil || merged == nil || merged.Amount == nil || *merged.Amount != 3 {
		t.Fatalf("per-turn merge = %+v err=%v", merged, err)
	}
	replaced, err := MergeProviderCredits(
		&ProviderCredit{Unit: "credit", Amount: &one, Provenance: CreditProvenancePerTurn},
		&ProviderCredit{Unit: "credit", Amount: &three, Provenance: CreditProvenanceCumulative},
	)
	if err != nil || replaced == nil || replaced.Amount == nil || *replaced.Amount != three {
		t.Fatalf("cumulative merge = %+v err=%v", replaced, err)
	}
	if _, err := MergeProviderCredits(&ProviderCredit{Unit: "credit", Amount: &one}, &ProviderCredit{Unit: "point", Amount: &two}); err == nil {
		t.Fatal("expected incompatible units")
	}
}

func TestParseResultRejectsInvalidCredits(t *testing.T) {
	for _, raw := range []string{
		`{"outputs":{},"measurements":{"credit":{"unit":"","amount":1}}}`,
		`{"outputs":{},"measurements":{"credit":{"unit":"credit","amount":-1}}}`,
	} {
		if _, err := ParseResult([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if result, err := ParseResult([]byte(`{"outputs":{},"measurements":{"credit":{"unit":"credit","amount":null}}}`)); err != nil || result.Measurements == nil || result.Measurements.Credit == nil || result.Measurements.Credit.Amount != nil {
		t.Fatalf("missing credit amount must remain unavailable: %+v err=%v", result.Measurements, err)
	}
	amount := 0.25
	result, err := ParseResult([]byte(`{"outputs":{},"measurements":{"credit":{"unit":"credit","amount":0.25}}}`))
	if err != nil || result.Measurements == nil || result.Measurements.Credit == nil || result.Measurements.Credit.Amount == nil || *result.Measurements.Credit.Amount != amount {
		t.Fatalf("valid credit rejected: %+v err=%v", result.Measurements, err)
	}
}
