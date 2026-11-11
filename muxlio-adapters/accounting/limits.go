package accounting

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"math/big"
)

type Limit struct {
	Kind      string `json:"kind"`
	Maximum   string `json:"maximum"`
	Provider  string `json:"provider,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Currency  string `json:"currency,omitempty"`
	Basis     string `json:"basis,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	Fidelity  string `json:"fidelity,omitempty"`
	OnMissing string `json:"onMissing,omitempty"`
}

func (l Limit) Key() contract.AccountingKey {
	return contract.AccountingKey{Kind: l.Kind, Provider: l.Provider, Unit: l.Unit, Currency: l.Currency, Basis: l.Basis}
}
func (l Limit) Validate() error {
	switch l.Kind {
	case "tokens":
		if l.Provider != "" || l.Unit != "" || l.Currency != "" || l.Basis != "" {
			return fmt.Errorf("token limit cannot specify economic units")
		}
		if err := contract.Count(l.Maximum).Validate(); err != nil {
			return err
		}
	case "credits":
		if l.Provider == "" || l.Unit == "" || l.Currency != "" || l.Basis != "" {
			return fmt.Errorf("credit limit requires provider and unit")
		}
		if err := contract.Amount(l.Maximum).Validate(); err != nil {
			return err
		}
	case "cost":
		if l.Currency == "" || l.Basis == "" || l.Unit != "" || l.Provider != "" {
			return fmt.Errorf("cost limit requires currency and basis")
		}
		if err := contract.Amount(l.Maximum).Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid accounting limit kind")
	}
	if l.Evidence != "" && l.Evidence != "collected-only" && l.Evidence != "collected-or-estimated" {
		return fmt.Errorf("invalid evidence policy")
	}
	if l.Fidelity != "" && l.Fidelity != "any" && l.Fidelity != "metered" {
		return fmt.Errorf("invalid fidelity policy")
	}
	if l.OnMissing != "" && l.OnMissing != "fail-open" && l.OnMissing != "fail-closed" {
		return fmt.Errorf("invalid missing accounting policy")
	}
	return (&contract.Accounting{Revision: 1, Coverage: []contract.MeterCoverage{{Key: l.Key(), State: "unknown"}}}).Validate()
}
func filterLimitMeasure[T any](m *contract.Measure[T], l Limit) bool {
	had := m.Collected != nil || m.Estimated != nil
	if m.Collected != nil && l.Fidelity == "metered" && m.Collected.Fidelity != "metered" {
		m.Collected = nil
	}
	if m.Estimated != nil && (l.Evidence != "collected-or-estimated" || l.Fidelity == "metered") {
		m.Estimated = nil
	}
	return had && m.Collected == nil && m.Estimated == nil
}

// CheckLimit uses the same reducer as summaries, with ineligible evidence
// removed before selection. Unknown work never becomes zero consumption.
func CheckLimit(views []AttemptView, l Limit, admission bool) error {
	if err := l.Validate(); err != nil {
		return err
	}
	filtered := make([]AttemptView, len(views))
	for i, v := range views {
		if coverageFor(v, l.Key()) == "inapplicable" && meterReported(v, l.Key()) {
			return fmt.Errorf("inconsistent-accounting: inapplicable %s meter reports readings", l.Kind)
		}
		filtered[i] = v
		if v.Accounting == nil {
			continue
		}
		raw, err := contract.CanonicalAccounting(*v.Accounting)
		if err != nil {
			return err
		}
		var a contract.Accounting
		if err = json.Unmarshal(raw, &a); err != nil {
			return err
		}
		removed := false
		for j := range a.Entries {
			e := &a.Entries[j]
			if l.Kind == "tokens" && e.Tokens != nil {
				t := e.Tokens
				for _, m := range []*contract.Measure[contract.Count]{&t.Total, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.Reasoning} {
					if filterLimitMeasure(m, l) {
						removed = true
					}
				}
			}
			for k := range e.Credits {
				c := &e.Credits[k]
				if l.Kind == "credits" && c.Provider == l.Provider && c.Unit == l.Unit {
					if filterLimitMeasure(&c.Amount, l) {
						removed = true
					}
				}
			}
			for k := range e.Costs {
				c := &e.Costs[k]
				if l.Kind == "cost" && c.Currency == l.Currency && c.Basis == l.Basis {
					if filterLimitMeasure(&c.Amount, l) {
						removed = true
					}
				}
			}
		}
		if removed {
			for j := range a.Coverage {
				if a.Coverage[j].Key == l.Key() && a.Coverage[j].State == "complete" {
					a.Coverage[j].State = "partial"
				}
			}
		}
		filtered[i].Accounting = &a
	}
	s, err := SummarizeAccounting(filtered)
	if err != nil {
		return err
	}
	value := SummaryAmount{Completeness: "unknown"}
	allInapplicable := len(filtered) > 0
	for _, v := range filtered {
		if coverageFor(v, l.Key()) != "inapplicable" || meterReported(v, l.Key()) {
			allInapplicable = false
			break
		}
	}
	if allInapplicable {
		return nil
	}
	switch l.Kind {
	case "tokens":
		value = s.Tokens
	case "credits":
		for _, v := range s.Credits {
			if v.Provider == l.Provider && v.Unit == l.Unit {
				value = v.Value
			}
		}
	case "cost":
		for _, v := range s.Costs {
			if v.Currency == l.Currency && v.Basis == l.Basis {
				value = v.Value
			}
		}
	}
	known := "0"
	if value.KnownSubtotal != nil {
		known = *value.KnownSubtotal
	}
	n, _ := new(big.Rat).SetString(known)
	max, _ := new(big.Rat).SetString(l.Maximum)
	cmp := n.Cmp(max)
	if cmp > 0 || (admission && cmp == 0) {
		return fmt.Errorf("budget-exhausted: %s", l.Kind)
	}
	if len(views) > 0 && l.OnMissing != "fail-open" && value.Completeness != "complete" && value.Completeness != "inapplicable" {
		return fmt.Errorf("missing-accounting: %s (%s)", l.Kind, value.Completeness)
	}
	return nil
}
