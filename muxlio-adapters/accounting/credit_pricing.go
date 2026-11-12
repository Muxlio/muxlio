package accounting

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"math/big"
	"net/url"
	"time"
)

// CreditTariff is an immutable reviewed replacement tariff, not a conversion
// between providers' credits or an assertion of incremental account spend.
type CreditTariff struct {
	ID             string          `json:"id"`
	Provider       string          `json:"provider"`
	Unit           string          `json:"unit"`
	Context        string          `json:"context"`
	SourceURL      string          `json:"sourceUrl"`
	ReviewedAt     time.Time       `json:"reviewedAt"`
	EffectiveFrom  time.Time       `json:"effectiveFrom"`
	EffectiveUntil time.Time       `json:"effectiveUntil,omitempty"`
	USDPerUnit     contract.Amount `json:"usdPerUnit"`
}

func PinCreditTariff(t CreditTariff) (CreditTariff, error) {
	if t.Provider == "" || t.Unit == "" || t.Context == "" || t.ReviewedAt.IsZero() || t.EffectiveFrom.IsZero() {
		return t, fmt.Errorf("credit tariff provenance incomplete")
	}
	u, err := url.Parse(t.SourceURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return t, fmt.Errorf("credit tariff source required")
	}
	if !t.EffectiveUntil.IsZero() && !t.EffectiveUntil.After(t.EffectiveFrom) {
		return t, fmt.Errorf("invalid credit tariff interval")
	}
	if err := t.USDPerUnit.Validate(); err != nil {
		return t, err
	}
	expected := t.ID
	t.ID = ""
	raw, err := json.Marshal(t)
	if err != nil {
		return t, err
	}
	t.ID = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if expected != "" && expected != t.ID {
		return t, fmt.Errorf("credit tariff checksum mismatch")
	}
	return t, nil
}

// EstimateCreditReplacement requires an explicitly verified pricing context
// and owned, collected units. A balance change or proxy is not sufficient.
func EstimateCreditReplacement(t CreditTariff, provider, unit, context string, at time.Time, reading *contract.Reading[contract.Amount]) (*contract.Reading[contract.Amount], error) {
	if t.ID == "" {
		return nil, fmt.Errorf("credit tariff must be pinned")
	}
	pinned, err := PinCreditTariff(t)
	if err != nil {
		return nil, err
	}
	if provider != t.Provider || unit != t.Unit || context != t.Context || at.IsZero() || at.Before(t.EffectiveFrom) || !t.EffectiveUntil.IsZero() && !at.Before(t.EffectiveUntil) {
		return nil, fmt.Errorf("credit tariff not applicable")
	}
	if reading == nil || reading.Fidelity != "metered" && reading.Fidelity != "derived-exact" {
		return nil, fmt.Errorf("owned collected credits required")
	}
	if err := reading.Value.Validate(); err != nil {
		return nil, err
	}
	units, _ := new(big.Rat).SetString(string(reading.Value))
	rate, _ := new(big.Rat).SetString(string(t.USDPerUnit))
	exact := new(big.Rat).Mul(units, rate)
	amount := roundHalfEven18(exact)
	if err := amount.Validate(); err != nil {
		return nil, err
	}
	if exact.Sign() > 0 && amount == "0" {
		return nil, fmt.Errorf("positive value below supported precision")
	}
	return &contract.Reading[contract.Amount]{Value: amount, Source: "muxlio-pricing", Fidelity: "derived-exact", Method: "credit-replacement", Version: "1", CatalogID: pinned.ID}, nil
}
