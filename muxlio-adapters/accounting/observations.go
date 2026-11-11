package accounting

import (
	"github.com/muxlio/muxlio-adapters/contract"
	"math/big"
)

// ObserveDelta records a decoder-confirmed disjoint event with stable identity.
func (r *Recorder) ObserveDelta(part Part) error { return r.Record(part) }

// ObserveCumulative replaces one identified cumulative scope. Omitted meters
// retain their confirmed readings. A decoder must use ReplaceScope for a full
// terminal replacement spanning multiple disjoint calls or model identities.
func (r *Recorder) ObserveCumulative(part Part) error {
	if r.final {
		return r.ReplaceScope(part.Scope, []Part{part})
	}
	entry, _, err := copyEntry(part.Entry)
	if err != nil {
		return r.reject("accounting_observation_invalid")
	}
	var prior *contract.AccountingEntry
	for _, p := range r.entries {
		if p.Scope != part.Scope {
			continue
		}
		if prior != nil || p.Entry.Provider != entry.Provider || p.Entry.Model != entry.Model {
			return r.reject("accounting_cumulative_scope_ambiguous")
		}
		copyE := p.Entry
		prior = &copyE
	}
	if prior != nil {
		if cumulativeRegressed(entry, *prior) {
			return r.reject("accounting_cumulative_counter_regressed")
		}
		if entry.Tokens == nil {
			entry.Tokens = prior.Tokens
		} else if prior.Tokens != nil {
			t, p := entry.Tokens, prior.Tokens
			changedDimensions := t.Input.Collected != nil || t.Input.Estimated != nil || t.Output.Collected != nil || t.Output.Estimated != nil
			t.Input = retainMeasure(t.Input, p.Input)
			t.Output = retainMeasure(t.Output, p.Output)
			t.CacheRead = retainMeasure(t.CacheRead, p.CacheRead)
			t.CacheWrite = retainMeasure(t.CacheWrite, p.CacheWrite)
			t.Reasoning = retainMeasure(t.Reasoning, p.Reasoning)
			if t.Total.Collected == nil && t.Total.Estimated == nil {
				t.Total = p.Total
				// An arithmetic total follows its component replacement; a
				// provider-reported conflicting total remains explicit evidence.
				if changedDimensions && p.Total.Collected != nil && p.Total.Collected.Fidelity == "derived-exact" && t.Input.Collected != nil && t.Output.Collected != nil {
					value, err := AddCount(t.Input.Collected.Value, t.Output.Collected.Value)
					if err != nil {
						return r.reject("accounting_overflow")
					}
					reading := *p.Total.Collected
					reading.Value = value
					t.Total = contract.Measure[contract.Count]{Collected: &reading}
				}
			}
		}
		for _, p := range prior.Credits {
			found := false
			for _, n := range entry.Credits {
				if n.Provider == p.Provider && n.Unit == p.Unit {
					found = true
					break
				}
			}
			if !found {
				entry.Credits = append(entry.Credits, p)
			}
		}
		for _, p := range prior.Costs {
			found := false
			for _, n := range entry.Costs {
				if n.Currency == p.Currency && n.Basis == p.Basis {
					found = true
					break
				}
			}
			if !found {
				entry.Costs = append(entry.Costs, p)
			}
		}
	}
	part.Entry = entry
	return r.ReplaceScope(part.Scope, []Part{part})
}

func readingRegressed[T ~string](next, prior contract.Measure[T]) bool {
	for _, pair := range [][2]*contract.Reading[T]{{next.Collected, prior.Collected}, {next.Estimated, prior.Estimated}} {
		if pair[0] == nil || pair[1] == nil {
			continue
		}
		n, okN := new(big.Rat).SetString(string(pair[0].Value))
		p, okP := new(big.Rat).SetString(string(pair[1].Value))
		if okN && okP && n.Cmp(p) < 0 {
			return true
		}
	}
	return false
}

func cumulativeRegressed(next, prior contract.AccountingEntry) bool {
	if next.Tokens != nil && prior.Tokens != nil {
		n, p := next.Tokens, prior.Tokens
		for _, pair := range [][2]contract.Measure[contract.Count]{{n.Total, p.Total}, {n.Input, p.Input}, {n.Output, p.Output}, {n.CacheRead, p.CacheRead}, {n.CacheWrite, p.CacheWrite}, {n.Reasoning, p.Reasoning}} {
			if readingRegressed(pair[0], pair[1]) {
				return true
			}
		}
	}
	for _, n := range next.Credits {
		for _, p := range prior.Credits {
			if n.Provider == p.Provider && n.Unit == p.Unit && readingRegressed(n.Amount, p.Amount) {
				return true
			}
		}
	}
	for _, n := range next.Costs {
		for _, p := range prior.Costs {
			if n.Currency == p.Currency && n.Basis == p.Basis && readingRegressed(n.Amount, p.Amount) {
				return true
			}
		}
	}
	return false
}

func retainMeasure[T ~string](next, prior contract.Measure[T]) contract.Measure[T] {
	if next.Collected == nil && next.Estimated == nil {
		return prior
	}
	return next
}
