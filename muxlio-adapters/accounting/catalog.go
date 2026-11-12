package accounting

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

// ModelPrice is one reviewed model rule. A nil rate means unknown; a pointer to
// "0" is an explicit zero rate.
type ModelPrice struct {
	SourceURL            string           `json:"sourceUrl"`
	ReviewedAt           time.Time        `json:"reviewedAt"`
	PricingContext       string           `json:"pricingContext"`
	Region               string           `json:"region"`
	Modality             string           `json:"modality"`
	ServiceTier          string           `json:"serviceTier,omitempty"`
	Mode                 string           `json:"mode,omitempty"`
	CacheWriteTTL        string           `json:"cacheWriteTtl,omitempty"`
	ContextMin           *int64           `json:"contextMin,omitempty"`
	ContextMax           *int64           `json:"contextMax,omitempty"`
	ReasoningUsesOutput  bool             `json:"reasoningUsesOutput,omitempty"`
	ExtraRates           []ChargeRate     `json:"extraRates,omitempty"`
	Provider             string           `json:"provider"`
	Model                string           `json:"model"`
	Currency             string           `json:"currency"`
	InputPerMillion      *contract.Amount `json:"inputPerMillion,omitempty"`
	OutputPerMillion     *contract.Amount `json:"outputPerMillion,omitempty"`
	CacheReadPerMillion  *contract.Amount `json:"cacheReadPerMillion,omitempty"`
	CacheWritePerMillion *contract.Amount `json:"cacheWritePerMillion,omitempty"`
	ReasoningPerMillion  *contract.Amount `json:"reasoningPerMillion,omitempty"`
	EffectiveFrom        time.Time        `json:"effectiveFrom,omitempty"`
	EffectiveUntil       time.Time        `json:"effectiveUntil,omitempty"`
	RuleID               string           `json:"ruleId"`
}
type ChargeRate struct {
	ID   string           `json:"id"`
	Kind string           `json:"kind"`
	Unit string           `json:"unit"`
	Per  contract.Count   `json:"per"`
	Rate *contract.Amount `json:"rate"`
}
type Catalog struct {
	ID     string       `json:"id"`
	Prices []ModelPrice `json:"prices"`
}

// PinCatalog validates a local reviewed snapshot and returns its immutable ID.
func PinCatalog(c Catalog) (Catalog, error) {
	expectedID := c.ID
	rawCopy, err := json.Marshal(c)
	if err != nil {
		return Catalog{}, err
	}
	if len(rawCopy) > 1<<20 {
		return Catalog{}, fmt.Errorf("price catalog exceeds 1 MiB")
	}
	if err = json.Unmarshal(rawCopy, &c); err != nil {
		return Catalog{}, err
	}
	if len(c.Prices) == 0 {
		return Catalog{}, fmt.Errorf("price catalog is empty")
	}
	sort.Slice(c.Prices, func(i, j int) bool {
		a, b := c.Prices[i], c.Prices[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.RuleID < b.RuleID
	})
	seen := map[string]bool{}
	for i := range c.Prices {
		p := &c.Prices[i]
		source, sourceErr := url.Parse(p.SourceURL)
		if sourceErr != nil || source.Scheme != "https" || source.Host == "" || p.ReviewedAt.IsZero() || p.EffectiveFrom.IsZero() || p.PricingContext == "" || p.Region == "" || p.Modality == "" {
			return Catalog{}, fmt.Errorf("price rule provenance and applicability required")
		}
		if p.Provider == "" || p.Model == "" || p.RuleID == "" || p.Currency == "" {
			return Catalog{}, fmt.Errorf("price rule identity is incomplete")
		}
		if p.Currency != "USD" {
			return Catalog{}, fmt.Errorf("API-equivalent catalog rates must be USD")
		}
		id := p.Provider + "\x00" + p.Model + "\x00" + p.RuleID
		if seen[id] {
			return Catalog{}, fmt.Errorf("duplicate price rule")
		}
		seen[id] = true
		if !regexp.MustCompile(`^[a-z0-9._:-]{1,80}$`).MatchString(p.RuleID) {
			return Catalog{}, fmt.Errorf("invalid price rule identifier")
		}
		if p.ContextMin != nil && *p.ContextMin < 0 || p.ContextMax != nil && *p.ContextMax <= 0 || p.ContextMin != nil && p.ContextMax != nil && *p.ContextMin >= *p.ContextMax {
			return Catalog{}, fmt.Errorf("invalid context pricing interval")
		}
		if !p.EffectiveUntil.IsZero() && !p.EffectiveFrom.IsZero() && !p.EffectiveUntil.After(p.EffectiveFrom) {
			return Catalog{}, fmt.Errorf("invalid price effective interval")
		}
		sort.Slice(p.ExtraRates, func(i, j int) bool { return p.ExtraRates[i].ID < p.ExtraRates[j].ID })
		charges := map[string]bool{}
		for _, rate := range p.ExtraRates {
			if !regexp.MustCompile(`^[a-z0-9._:-]{1,80}$`).MatchString(rate.ID) || rate.Kind == "" || rate.Unit == "" || charges[rate.ID] || rate.Rate == nil {
				return Catalog{}, fmt.Errorf("invalid extra-charge rate")
			}
			charges[rate.ID] = true
			if err := rate.Per.Validate(); err != nil || rate.Per == "0" {
				return Catalog{}, fmt.Errorf("extra-charge denominator must be positive")
			}
			if err := rate.Rate.Validate(); err != nil {
				return Catalog{}, err
			}
		}
		for _, r := range []*contract.Amount{p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion, p.ReasoningPerMillion} {
			if r != nil {
				if err := r.Validate(); err != nil {
					return Catalog{}, err
				}
			}
		}
	}
	c.ID = ""
	raw, err := json.Marshal(c)
	if err != nil {
		return Catalog{}, err
	}
	sum := sha256.Sum256(raw)
	id := fmt.Sprintf("sha256:%x", sum)
	if expectedID != "" && expectedID != id {
		return Catalog{}, fmt.Errorf("price catalog checksum mismatch")
	}
	c.ID = id
	return c, nil
}

// LoadCatalog accepts an explicitly pinned local snapshot, never an online
// price lookup. Historic estimates retain this digest even after a refresh.
func LoadCatalog(raw []byte, expectedID string) (Catalog, error) {
	if len(raw) > 1<<20 || expectedID == "" {
		return Catalog{}, fmt.Errorf("bounded catalog and expected checksum required")
	}
	var c Catalog
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Catalog{}, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Catalog{}, fmt.Errorf("catalog must contain one JSON object")
	}
	if c.ID != "" && c.ID != expectedID {
		return Catalog{}, fmt.Errorf("catalog checksum mismatch")
	}
	c.ID = expectedID
	return PinCatalog(c)
}
