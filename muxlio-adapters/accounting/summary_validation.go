package accounting

import (
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
)

// ValidateSummary checks imported canonical summaries without deriving values
// from unrelated report fields or accepting binary floating-point amounts.
func ValidateSummary(s AccountingSummary) error {
	validate := func(v SummaryAmount, count bool) error {
		if v.Evidence != nil {
			for _, list := range [][]string{v.Evidence.Collected, v.Evidence.Estimated} {
				seen := map[string]bool{}
				for _, f := range list {
					if (f != "metered" && f != "derived-exact" && f != "provider-estimate") || seen[f] {
						return fmt.Errorf("invalid summary evidence")
					}
					seen[f] = true
				}
			}
		}
		switch v.Completeness {
		case "complete", "partial", "unknown", "inapplicable":
		default:
			return fmt.Errorf("invalid accounting completeness")
		}
		for _, n := range []int{v.Attempts.Complete, v.Attempts.Partial, v.Attempts.Unknown, v.Attempts.Pending, v.Attempts.Inapplicable} {
			if n < 0 {
				return fmt.Errorf("negative accounting coverage count")
			}
		}
		if v.Completeness != summaryCompleteness(v.Attempts) {
			return fmt.Errorf("accounting completeness does not match coverage counts")
		}
		for _, n := range []*string{v.CollectedSubtotal, v.EstimatedFallbackSubtotal, v.KnownSubtotal, v.EffectiveTotal} {
			if n != nil {
				if count {
					if err := contract.Count(*n).Validate(); err != nil {
						return err
					}
				} else if err := contract.Amount(*n).Validate(); err != nil {
					return err
				}
			}
		}
		if v.EffectiveTotal != nil && (v.Completeness != "complete" || v.KnownSubtotal == nil || *v.EffectiveTotal != *v.KnownSubtotal || v.Attempts.Pending > 0) {
			return fmt.Errorf("effective total requires complete closed coverage")
		}
		hasContributions := v.CollectedSubtotal != nil || v.EstimatedFallbackSubtotal != nil
		if hasContributions != (v.KnownSubtotal != nil) {
			return fmt.Errorf("known subtotal requires collected or estimated contributions")
		}
		if hasContributions && v.Completeness == "inapplicable" {
			return fmt.Errorf("inapplicable summary cannot contain consumption")
		}
		if v.Completeness == "complete" && v.KnownSubtotal == nil {
			return fmt.Errorf("complete summary requires a known total")
		}
		if v.KnownSubtotal != nil && v.Completeness == "complete" && v.EffectiveTotal == nil {
			return fmt.Errorf("complete known subtotal requires an effective total")
		}
		if v.CollectedSubtotal != nil || v.EstimatedFallbackSubtotal != nil {
			a, b := valueOrZero(v.CollectedSubtotal), valueOrZero(v.EstimatedFallbackSubtotal)
			var sum string
			if count {
				n, err := AddCount(contract.Count(a), contract.Count(b))
				if err != nil {
					return err
				}
				sum = string(n)
			} else {
				n, err := AddAmount(contract.Amount(a), contract.Amount(b))
				if err != nil {
					return err
				}
				sum = string(n)
			}
			if v.KnownSubtotal == nil || *v.KnownSubtotal != sum {
				return fmt.Errorf("known subtotal does not match disjoint contributions")
			}
		}
		return nil
	}
	for _, v := range []SummaryAmount{s.Tokens, s.TokenDimensions.Input, s.TokenDimensions.Output, s.TokenDimensions.CacheRead, s.TokenDimensions.CacheWrite, s.TokenDimensions.Reasoning} {
		if err := validate(v, true); err != nil {
			return err
		}
	}
	if s.ReferenceValue != nil {
		if err := validate(s.ReferenceValue.SummaryAmount, false); err != nil {
			return err
		}
		seenMethods := map[string]bool{}
		for _, method := range s.ReferenceValue.Methods {
			if (method != "provider-quote" && method != "provider-metered" && method != "published-token-rates" && method != "credit-replacement") || seenMethods[method] {
				return fmt.Errorf("invalid reference method")
			}
			seenMethods[method] = true
		}
	}
	seen := map[contract.AccountingKey]bool{}
	for _, v := range s.Credits {
		key := contract.AccountingKey{Kind: "credits", Provider: v.Provider, Unit: v.Unit}
		if seen[key] {
			return fmt.Errorf("duplicate credit summary")
		}
		seen[key] = true
		if err := (&contract.Accounting{Revision: 1, Coverage: []contract.MeterCoverage{{Key: key, State: "complete"}}}).Validate(); err != nil {
			return err
		}
		if err := validate(v.Value, false); err != nil {
			return err
		}
	}
	for _, v := range s.Costs {
		key := contract.AccountingKey{Kind: "cost", Currency: v.Currency, Basis: v.Basis}
		if seen[key] {
			return fmt.Errorf("duplicate cost summary")
		}
		seen[key] = true
		if err := (&contract.Accounting{Revision: 1, Coverage: []contract.MeterCoverage{{Key: key, State: "complete"}}}).Validate(); err != nil {
			return err
		}
		if err := validate(v.Value, false); err != nil {
			return err
		}
	}
	if s.Unattributed.Credits.UnknownAttempts < 0 || s.Unattributed.Credits.PendingAttempts < 0 || s.Unattributed.Costs.UnknownAttempts < 0 || s.Unattributed.Costs.PendingAttempts < 0 {
		return fmt.Errorf("negative unattributed coverage")
	}
	return nil
}
