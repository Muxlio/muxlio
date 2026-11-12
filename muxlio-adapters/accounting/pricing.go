package accounting

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

type PriceRequest struct {
	ModelRole                                       string
	PricingContext                                  string
	Region                                          string
	Modality                                        string
	ServiceTier                                     string
	Mode                                            string
	CacheWriteTTL                                   string
	ContextSize                                     *int64
	ExtraCharges                                    []Charge
	ChargeApplicability                             map[string]bool
	Provider                                        string
	Model                                           string
	Currency                                        string
	At                                              time.Time
	Input, Output, CacheRead, CacheWrite, Reasoning *int64
	ChargesComplete                                 bool
}
type Charge struct {
	RateID   string
	Kind     string
	Unit     string
	Quantity contract.Amount
}
type PriceResult struct {
	Amount    contract.Amount
	CatalogID string
	RuleID    string
}

// EstimateAPIEquivalent prices one fully described model request. Missing
// categories, model identity or fee-eligibility evidence are never treated as 0.
func EstimateAPIEquivalent(c Catalog, q PriceRequest) (PriceResult, error) {
	pinned, err := PinCatalog(c)
	if err != nil {
		return PriceResult{}, err
	}
	if c.ID == "" {
		return PriceResult{}, fmt.Errorf("unpriceable: catalog is not pinned")
	}
	c = pinned
	if q.ContextSize != nil && *q.ContextSize < 0 {
		return PriceResult{}, fmt.Errorf("unpriceable: negative context size")
	}
	if q.Provider == "" || q.Model == "" || q.Model == "auto" || q.ModelRole != "served" {
		return PriceResult{}, fmt.Errorf("unpriceable: actual provider/model unavailable")
	}
	if q.Input == nil || q.Output == nil || q.CacheRead == nil || q.CacheWrite == nil || q.Reasoning == nil || !q.ChargesComplete {
		return PriceResult{}, fmt.Errorf("unpriceable: request accounting is incomplete")
	}
	for _, v := range []*int64{q.Input, q.Output, q.CacheRead, q.CacheWrite, q.Reasoning} {
		if *v < 0 {
			return PriceResult{}, fmt.Errorf("unpriceable: negative token quantity")
		}
	}
	if *q.CacheRead+*q.CacheWrite < *q.CacheRead || *q.CacheRead+*q.CacheWrite > *q.Input || *q.Reasoning > *q.Output {
		return PriceResult{}, fmt.Errorf("unpriceable: invalid inclusive token dimensions")
	}
	var rule *ModelPrice
	for i := range c.Prices {
		p := &c.Prices[i]
		if p.Provider == q.Provider && p.Model == q.Model {
			if (p.ContextMin != nil || p.ContextMax != nil) && q.ContextSize == nil {
				return PriceResult{}, fmt.Errorf("unpriceable: request context size required")
			}
			if (!p.EffectiveFrom.IsZero() || !p.EffectiveUntil.IsZero()) && q.At.IsZero() {
				return PriceResult{}, fmt.Errorf("unpriceable: request time required")
			}
		}
		if p.Provider == q.Provider && p.Model == q.Model && p.PricingContext == q.PricingContext && p.Region == q.Region && p.Modality == q.Modality && p.ServiceTier == q.ServiceTier && p.Mode == q.Mode && p.CacheWriteTTL == q.CacheWriteTTL && (p.ContextMin == nil || q.ContextSize != nil && *q.ContextSize >= *p.ContextMin) && (p.ContextMax == nil || q.ContextSize != nil && *q.ContextSize < *p.ContextMax) && (q.Currency == "" || q.Currency == p.Currency) && (q.At.IsZero() || ((p.EffectiveFrom.IsZero() || !q.At.Before(p.EffectiveFrom)) && (p.EffectiveUntil.IsZero() || q.At.Before(p.EffectiveUntil)))) {
			if rule != nil {
				return PriceResult{}, fmt.Errorf("unpriceable: ambiguous catalog rules")
			}
			rule = p
		}
	}
	if rule == nil {
		return PriceResult{}, fmt.Errorf("unpriceable: model price not found")
	}
	uncached := *q.Input - *q.CacheRead - *q.CacheWrite
	visible := *q.Output - *q.Reasoning
	reasoningRate := rule.ReasoningPerMillion
	if rule.ReasoningUsesOutput {
		reasoningRate = rule.OutputPerMillion
	}
	items := []struct {
		n int64
		r *contract.Amount
	}{{uncached, rule.InputPerMillion}, {*q.CacheRead, rule.CacheReadPerMillion}, {*q.CacheWrite, rule.CacheWritePerMillion}, {visible, rule.OutputPerMillion}, {*q.Reasoning, reasoningRate}}
	sum := new(big.Rat)
	for _, it := range items {
		if it.r == nil {
			if it.n > 0 {
				return PriceResult{}, fmt.Errorf("unpriceable: applicable category has no catalog rate")
			}
			continue
		}
		if err := it.r.Validate(); err != nil {
			return PriceResult{}, err
		}
		rate, ok := new(big.Rat).SetString(string(*it.r))
		if !ok {
			return PriceResult{}, fmt.Errorf("invalid catalog rate")
		}
		term := new(big.Rat).Mul(rate, big.NewRat(it.n, 1_000_000))
		sum.Add(sum, term)
	}
	charges := map[string]Charge{}
	knownRates := map[string]bool{}
	for _, rate := range rule.ExtraRates {
		knownRates[rate.ID] = true
	}
	for id, applies := range q.ChargeApplicability {
		if applies && !knownRates[id] {
			return PriceResult{}, fmt.Errorf("unpriceable: applicable extra charge has no catalog rate")
		}
	}
	for _, charge := range q.ExtraCharges {
		if _, duplicate := charges[charge.RateID]; duplicate {
			return PriceResult{}, fmt.Errorf("unpriceable: duplicate extra charge")
		}
		if err := charge.Quantity.Validate(); err != nil {
			return PriceResult{}, err
		}
		charges[charge.RateID] = charge
	}
	for _, rate := range rule.ExtraRates {
		applies, known := q.ChargeApplicability[rate.ID]
		if !known {
			return PriceResult{}, fmt.Errorf("unpriceable: extra-charge applicability unknown")
		}
		charge, present := charges[rate.ID]
		delete(charges, rate.ID)
		if !applies {
			if present && charge.Quantity != "0" {
				return PriceResult{}, fmt.Errorf("unpriceable: inapplicable charge has quantity")
			}
			continue
		}
		if !present || charge.Kind != rate.Kind || charge.Unit != rate.Unit {
			return PriceResult{}, fmt.Errorf("unpriceable: extra-charge quantity or unit missing")
		}
		quantity, _ := new(big.Rat).SetString(string(charge.Quantity))
		price, _ := new(big.Rat).SetString(string(*rate.Rate))
		per, _ := new(big.Rat).SetString(string(rate.Per))
		sum.Add(sum, new(big.Rat).Quo(new(big.Rat).Mul(quantity, price), per))
	}
	if len(charges) > 0 {
		return PriceResult{}, fmt.Errorf("unpriceable: extra charge has no catalog rate")
	}
	amount := roundHalfEven18(sum)
	if sum.Sign() > 0 && amount == "0" {
		return PriceResult{}, fmt.Errorf("unpriceable: positive value below supported precision")
	}
	if err := amount.Validate(); err != nil {
		return PriceResult{}, fmt.Errorf("unpriceable: calculated amount exceeds supported precision")
	}
	return PriceResult{Amount: amount, CatalogID: c.ID, RuleID: rule.RuleID}, nil
}

func roundHalfEven18(value *big.Rat) contract.Amount {
	negative := value.Sign() < 0
	if negative {
		value = new(big.Rat).Neg(value)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	n := new(big.Int).Mul(value.Num(), scale)
	q, r := new(big.Int).QuoRem(n, value.Denom(), new(big.Int))
	twice := new(big.Int).Lsh(r, 1)
	cmp := twice.Cmp(value.Denom())
	if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	s := q.String()
	if len(s) <= 18 {
		s = strings.Repeat("0", 19-len(s)) + s
	}
	whole, frac := s[:len(s)-18], strings.TrimRight(s[len(s)-18:], "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if negative {
		out = "-" + out
	}
	return contract.Amount(out)
}
