package accounting

import (
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"reflect"
)

func mergeScopeMeasure(a, b contract.Measure[contract.Count]) (contract.Measure[contract.Count], error) {
	merge := func(left, right *contract.Reading[contract.Count]) (*contract.Reading[contract.Count], error) {
		if right == nil {
			return left, nil
		}
		if left == nil {
			v := *right
			return &v, nil
		}
		l, r := *left, *right
		l.Value, r.Value = "0", "0"
		if !reflect.DeepEqual(l, r) {
			return nil, fmt.Errorf("scope provenance is ambiguous")
		}
		v, err := AddCount(left.Value, right.Value)
		if err != nil {
			return nil, err
		}
		out := *left
		out.Value = v
		return &out, nil
	}
	var err error
	a.Collected, err = merge(a.Collected, b.Collected)
	if err != nil {
		return a, err
	}
	a.Estimated, err = merge(a.Estimated, b.Estimated)
	return a, err
}

// ReplaceTokenScope applies a certified aggregate over a token-only scope.
// Missing dimensions retain their confirmed subtotals across disjoint events.
// Economic readings cannot be erased through this token-specific operation.
func (r *Recorder) ReplaceTokenScope(scope string, entry contract.AccountingEntry) error {
	entry, _, err := copyEntry(entry)
	if err != nil {
		return r.reject("accounting_observation_invalid")
	}
	if entry.Tokens == nil || len(entry.Credits) > 0 || len(entry.Costs) > 0 {
		return r.reject("accounting_scope_invalid")
	}
	t := entry.Tokens
	left := []*contract.Measure[contract.Count]{&t.Total, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.Reasoning}
	missing := make([]bool, len(left))
	for i, m := range left {
		missing[i] = m.Collected == nil && m.Estimated == nil
	}
	// A prior total cannot survive changed components as a second presentation.
	componentsChanged := !missing[1] || !missing[2]
	for _, p := range r.entries {
		if p.Scope != scope {
			continue
		}
		if len(p.Entry.Credits) > 0 || len(p.Entry.Costs) > 0 {
			return r.reject("accounting_scope_invalid")
		}
		if p.Entry.Tokens == nil {
			continue
		}
		u := p.Entry.Tokens
		right := []contract.Measure[contract.Count]{u.Total, u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Reasoning}
		for i, m := range right {
			if !missing[i] || i == 0 && componentsChanged {
				continue
			}
			*left[i], err = mergeScopeMeasure(*left[i], m)
			if err != nil {
				return r.reject("accounting_scope_provenance_ambiguous")
			}
		}
	}
	if missing[0] && t.Input.Collected != nil && t.Output.Collected != nil {
		total, err := AddCount(t.Input.Collected.Value, t.Output.Collected.Value)
		if err != nil {
			return r.reject("accounting_overflow")
		}
		t.Total = contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: total, Source: "muxlio-scope-fold", Fidelity: "derived-exact"}}
	}
	return r.ReplaceScope(scope, []Part{{Scope: scope, Entry: entry}})
}
