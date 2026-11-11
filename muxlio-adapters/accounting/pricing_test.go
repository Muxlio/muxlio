package accounting

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/contract"
	"math/big"
	"testing"
)

func rate(v string) *contract.Amount { a := contract.Amount(v); return &a }
func number(v int64) *int64          { return &v }
func pricedFixture(t *testing.T) (Catalog, PriceRequest) {
	t.Helper()
	c, err := PinCatalog(Catalog{Prices: []ModelPrice{{Provider: "fixture", Model: "model", Currency: "USD", RuleID: "standard-5m", ServiceTier: "default", Mode: "standard", CacheWriteTTL: "5m", ContextMin: number(0), ContextMax: number(200000), InputPerMillion: rate("3"), OutputPerMillion: rate("15"), CacheReadPerMillion: rate("0.3"), CacheWritePerMillion: rate("3.75"), ReasoningPerMillion: rate("20"), ExtraRates: []ChargeRate{{ID: "search", Kind: "search", Unit: "call", Per: "1", Rate: rate("0.01")}}}}})
	if err != nil {
		t.Fatal(err)
	}
	q := PriceRequest{Provider: "fixture", Model: "model", ServiceTier: "default", Mode: "standard", CacheWriteTTL: "5m", ContextSize: number(1000), Input: number(1000), Output: number(200), CacheRead: number(200), CacheWrite: number(100), Reasoning: number(50), ChargesComplete: true, ChargeApplicability: map[string]bool{"search": true}, ExtraCharges: []Charge{{RateID: "search", Kind: "search", Unit: "call", Quantity: "2"}}}
	return c, q
}
func TestPricingDisjointBucketsAndExactExtraCharges(t *testing.T) {
	c, q := pricedFixture(t)
	p, err := EstimateAPIEquivalent(c, q)
	if err != nil || p.Amount != "0.025785" || p.CatalogID != c.ID {
		t.Fatalf("%+v %v", p, err)
	}
	q.ChargeApplicability["search"] = false
	q.ExtraCharges = nil
	p, err = EstimateAPIEquivalent(c, q)
	if err != nil || p.Amount != "0.005785" {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestPricingMissingEvidenceIsUnpriceable(t *testing.T) {
	for _, change := range []func(*PriceRequest){func(q *PriceRequest) { q.Model = "auto" }, func(q *PriceRequest) { q.CacheWriteTTL = "" }, func(q *PriceRequest) { q.ContextSize = nil }, func(q *PriceRequest) { q.ServiceTier = "" }, func(q *PriceRequest) { q.Mode = "batch" }, func(q *PriceRequest) { q.ChargesComplete = false }, func(q *PriceRequest) { q.ChargeApplicability = nil }, func(q *PriceRequest) { q.ExtraCharges = nil }, func(q *PriceRequest) { q.CacheRead = nil }} {
		c, q := pricedFixture(t)
		change(&q)
		if _, err := EstimateAPIEquivalent(c, q); err == nil {
			t.Fatal("incomplete pricing request accepted")
		}
	}
}
func TestPricingContextAndModeRules(t *testing.T) {
	c, q := pricedFixture(t)
	second := c.Prices[0]
	second.RuleID = "priority-1h-long"
	second.Mode = "priority"
	second.CacheWriteTTL = "1h"
	second.ContextMin = number(200000)
	second.ContextMax = nil
	second.InputPerMillion = rate("6")
	second.CacheWritePerMillion = rate("12")
	c.ID = ""
	c.Prices = append(c.Prices, second)
	var err error
	c, err = PinCatalog(c)
	if err != nil {
		t.Fatal(err)
	}
	q.Mode = "priority"
	q.CacheWriteTTL = "1h"
	q.ContextSize = number(200000)
	p, err := EstimateAPIEquivalent(c, q)
	if err != nil || p.RuleID != second.RuleID || p.Amount != "0.02871" {
		t.Fatalf("%+v %v", p, err)
	}
}
func TestPricingHalfEvenAndCatalogIntegrity(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want contract.Amount
	}{{1, "0"}, {3, "0.000000000000000002"}, {5, "0.000000000000000002"}} {
		r := new(big.Rat).SetFrac(big.NewInt(tc.n), new(big.Int).Mul(big.NewInt(2), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
		if got := roundHalfEven18(r); got != tc.want {
			t.Fatalf("%s != %s", got, tc.want)
		}
	}
	c, _ := pricedFixture(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = LoadCatalog(raw, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadCatalog(raw, "sha256:wrong"); err == nil {
		t.Fatal("catalog checksum ignored")
	}
	if _, err = LoadCatalog(raw, ""); err == nil {
		t.Fatal("unpinned catalog accepted")
	}
}
func TestRecordPricedRetainsCollectedAlternativeOnce(t *testing.T) {
	c, q := pricedFixture(t)
	r := NewRecorder()
	part := Part{Scope: "call", Entry: contract.AccountingEntry{ID: "call-1", Costs: []contract.CostMeasure{{Currency: "USD", Basis: "api-equivalent", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "0.02", Source: "provider", Fidelity: "provider-estimate"}}}}}}
	if _, err := r.RecordPriced(part, c, q); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	cost := snapshot.Entries[0].Costs[0]
	if cost.Amount.Estimated == nil || cost.Amount.Estimated.Value != "0.025785" || cost.Amount.Estimated.CatalogID != c.ID {
		t.Fatal("per-call catalog provenance lost")
	}
	summary, err := SummarizeAccounting([]AttemptView{{State: "succeeded", Accounting: snapshot}})
	if err != nil || *summary.Costs[0].Value.KnownSubtotal != "0.02" || summary.Costs[0].Value.EstimatedFallbackSubtotal != nil {
		t.Fatalf("alternatives added: %+v %v", summary, err)
	}
}
