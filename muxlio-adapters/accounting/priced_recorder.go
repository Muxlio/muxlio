package accounting

import (
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
)

// RecordPriced evaluates each disjoint model call before compaction. Catalog
// estimates and collected API-equivalent evidence occupy the same measure.
func (r *Recorder) RecordPriced(part Part, catalog Catalog, request PriceRequest) (PriceResult, error) {
	// A directly reported attributable USD quote needs no local estimate.
	if cost, method := referenceCost(part.Entry); cost != nil && (method == "provider-quote" || method == "provider-metered") {
		if err := r.Record(part); err != nil {
			return PriceResult{}, err
		}
		return PriceResult{Amount: cost.Amount.Collected.Value}, nil
	}
	price, err := EstimateAPIEquivalent(catalog, request)
	if err != nil {
		if recordErr := r.Record(part); recordErr != nil {
			return PriceResult{}, recordErr
		}
		_ = r.SetCoverage(contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: "api-equivalent"}, "unknown", "unpriceable")
		return PriceResult{}, err
	}
	if part.Entry.Provider != "" && part.Entry.Provider != request.Provider || part.Entry.Model != "" && part.Entry.Model != request.Model {
		return PriceResult{}, fmt.Errorf("price request differs from served model identity")
	}
	part.Entry.Provider, part.Entry.Model = request.Provider, request.Model
	estimate := &contract.Reading[contract.Amount]{Value: price.Amount, Source: "muxlio-pricing", Fidelity: "derived-exact", Method: "catalog-v1:" + price.RuleID, Version: "1", CatalogID: price.CatalogID}
	found := false
	for i := range part.Entry.Costs {
		cost := &part.Entry.Costs[i]
		if cost.Currency == "USD" && cost.Basis == "api-equivalent" {
			cost.Amount.Estimated = estimate
			found = true
		}
	}
	if !found {
		part.Entry.Costs = append(part.Entry.Costs, contract.CostMeasure{Currency: "USD", Basis: "api-equivalent", Amount: contract.Measure[contract.Amount]{Estimated: estimate}})
	}
	if err = r.Record(part); err != nil {
		return PriceResult{}, err
	}
	key := contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: "api-equivalent"}
	if _, exists := r.coverage[key]; !exists {
		_ = r.SetCoverage(key, "partial", "source-coverage-unverified")
	}
	return price, nil
}
