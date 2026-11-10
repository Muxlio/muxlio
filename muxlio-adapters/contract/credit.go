package contract

import (
	"fmt"
	"math"
	"strings"
)

const (
	CreditProvenancePerTurn     = "per-turn"
	CreditProvenanceCumulative = "cumulative"
)

// ProviderCredit carries provider-reported credits independently of token usage
// and USD costs. A nil Amount means unavailable within this record; a non-nil
// Amount is a reported value, including zero. Credits are never inferred from
// tokens, remaining quota, or context occupancy.
type ProviderCredit struct {
	Unit       string   `json:"unit"`
	Amount     *float64 `json:"amount,omitempty"`
	Provenance string   `json:"provenance,omitempty"`
}

func normalizeCreditUnit(unit string) string { return strings.ToLower(strings.TrimSpace(unit)) }

func validateProviderCredit(c *ProviderCredit) error {
	if c == nil {
		return nil
	}
	if normalizeCreditUnit(c.Unit) == "" {
		return fmt.Errorf("credit unit must be non-empty")
	}
	switch c.Provenance {
	case "", CreditProvenancePerTurn, CreditProvenanceCumulative:
	default:
		return fmt.Errorf("unsupported credit provenance %q", c.Provenance)
	}
	if c.Amount != nil {
		v := *c.Amount
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("credit amount must be finite and non-negative")
		}
	}
	return nil
}

func addCreditAmount(total, add float64) (float64, error) {
	if add < 0 {
		return 0, fmt.Errorf("credit amount must be non-negative")
	}
	next := total + add
	if next < total || next > math.MaxFloat64 {
		return 0, fmt.Errorf("credit amount overflow")
	}
	return next, nil
}

// MergeProviderCredits combines credit observations from successive executor
// events. Cumulative totals replace any prior amount for the same unit;
// per-turn totals add. Incompatible unit identities are rejected.
func MergeProviderCredits(prior, next *ProviderCredit) (*ProviderCredit, error) {
	if next == nil {
		return prior, nil
	}
	if err := validateProviderCredit(next); err != nil {
		return nil, err
	}
	if prior == nil {
		copy := *next
		copy.Unit = normalizeCreditUnit(copy.Unit)
		return &copy, nil
	}
	if err := validateProviderCredit(prior); err != nil {
		return nil, err
	}
	left, right := normalizeCreditUnit(prior.Unit), normalizeCreditUnit(next.Unit)
	if left != right {
		return nil, fmt.Errorf("incompatible credit units %q and %q", prior.Unit, next.Unit)
	}
	merged := ProviderCredit{Unit: left, Provenance: prior.Provenance}
	if merged.Provenance == "" {
		merged.Provenance = CreditProvenancePerTurn
	}
	if next.Provenance == CreditProvenanceCumulative {
		merged.Provenance = CreditProvenanceCumulative
	}
	switch {
	case next.Provenance == CreditProvenanceCumulative:
		if next.Amount != nil {
			v := *next.Amount
			merged.Amount = &v
		} else if prior.Amount != nil {
			v := *prior.Amount
			merged.Amount = &v
		}
	case prior.Provenance == CreditProvenanceCumulative && prior.Amount != nil:
		v := *prior.Amount
		merged.Amount = &v
	case prior.Amount != nil && next.Amount != nil:
		sum, err := addCreditAmount(*prior.Amount, *next.Amount)
		if err != nil {
			return nil, err
		}
		merged.Amount = &sum
	case next.Amount != nil:
		v := *next.Amount
		merged.Amount = &v
	case prior.Amount != nil:
		v := *prior.Amount
		merged.Amount = &v
	}
	return &merged, nil
}
