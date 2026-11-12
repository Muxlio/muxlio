package accounting

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"testing"
	"time"
)

func TestCreditTariffProvenanceApplicabilityAndPrecision(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	tariff, err := PinCreditTariff(CreditTariff{Provider: "fixture", Unit: "credit", Context: "commercial", SourceURL: "https://provider.example/pricing", ReviewedAt: at, EffectiveFrom: at, EffectiveUntil: at.Add(time.Hour), USDPerUnit: "0.04"})
	if err != nil {
		t.Fatal(err)
	}
	reading := &contract.Reading[contract.Amount]{Value: "0.18065595854063016", Source: "provider-turn", Fidelity: "metered"}
	value, err := EstimateCreditReplacement(tariff, "fixture", "credit", "commercial", at, reading)
	if err != nil || value.Value != "0.007226238341625206" || value.CatalogID != tariff.ID {
		t.Fatalf("%+v %v", value, err)
	}
	for _, tc := range []struct {
		provider, unit, context string
		at                      time.Time
	}{
		{"other", "credit", "commercial", at}, {"fixture", "other", "commercial", at}, {"fixture", "credit", "unknown", at},
		{"fixture", "credit", "commercial", at.Add(-time.Second)}, {"fixture", "credit", "commercial", at.Add(time.Hour)},
		{"fixture", "credit", "commercial", time.Time{}},
	} {
		if _, err := EstimateCreditReplacement(tariff, tc.provider, tc.unit, tc.context, tc.at, reading); err == nil {
			t.Fatalf("inapplicable tariff accepted: %+v", tc)
		}
	}
	for _, fidelity := range []string{"proxy", "provider-estimate"} {
		copy := *reading
		copy.Fidelity = fidelity
		if _, err := EstimateCreditReplacement(tariff, "fixture", "credit", "commercial", at, &copy); err == nil {
			t.Fatal("unmetered credit evidence priced")
		}
	}
	changed := tariff
	changed.USDPerUnit = "0.05"
	if _, err := PinCreditTariff(changed); err == nil {
		t.Fatal("tariff mutation ignored")
	}
	unpinned := tariff
	unpinned.ID = ""
	if _, err := EstimateCreditReplacement(unpinned, "fixture", "credit", "commercial", at, reading); err == nil {
		t.Fatal("unpinned rate accepted")
	}
	// Exact ties round once at the charge boundary, with half-even behavior.
	half, _ := PinCreditTariff(CreditTariff{Provider: "fixture", Unit: "credit", Context: "commercial", SourceURL: tariff.SourceURL, ReviewedAt: at, EffectiveFrom: at, USDPerUnit: "0.5"})
	for _, tc := range []struct{ units, want contract.Amount }{{"0.000000000000000003", "0.000000000000000002"}, {"0.000000000000000005", "0.000000000000000002"}} {
		copy := *reading
		copy.Value = tc.units
		got, err := EstimateCreditReplacement(half, "fixture", "credit", "commercial", at, &copy)
		if err != nil || got.Value != tc.want {
			t.Fatalf("half-even result: %+v %v", got, err)
		}
	}
}
