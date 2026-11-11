package accounting

import (
	"crypto/sha256"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
)

// A bucket includes meter presence and all pricing/provenance metadata. Values
// and call IDs are excluded only after each individual call has been priced.
func compactEntry(entry contract.AccountingEntry) (string, contract.AccountingEntry, error) {
	copyE, _, err := copyEntry(entry)
	if err != nil {
		return "", entry, err
	}
	copyE.ID = "bucket"
	zeroCount := func(m *contract.Measure[contract.Count]) {
		if m.Collected != nil {
			m.Collected.Value = "0"
		}
		if m.Estimated != nil {
			m.Estimated.Value = "0"
		}
	}
	zeroAmount := func(m *contract.Measure[contract.Amount]) {
		if m.Collected != nil {
			m.Collected.Value = "0"
		}
		if m.Estimated != nil {
			m.Estimated.Value = "0"
		}
	}
	if t := copyE.Tokens; t != nil {
		for _, m := range []*contract.Measure[contract.Count]{&t.Total, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.Reasoning} {
			zeroCount(m)
		}
	}
	for i := range copyE.Credits {
		zeroAmount(&copyE.Credits[i].Amount)
	}
	for i := range copyE.Costs {
		zeroAmount(&copyE.Costs[i].Amount)
	}
	_, raw, err := copyEntry(copyE)
	if err != nil {
		return "", entry, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256(raw))
	entry.ID = "bucket-" + key
	return key, entry, nil
}
func addMeasure[T ~string](a, b contract.Measure[T], add func(T, T) (T, error)) (contract.Measure[T], error) {
	for _, pair := range [][2]*contract.Reading[T]{{a.Collected, b.Collected}, {a.Estimated, b.Estimated}} {
		if pair[0] == nil && pair[1] == nil {
			continue
		}
		if pair[0] == nil || pair[1] == nil {
			return a, fmt.Errorf("incompatible accounting bucket")
		}
		v, err := add(pair[0].Value, pair[1].Value)
		if err != nil {
			return a, err
		}
		pair[0].Value = v
	}
	return a, nil
}
func addEntries(a, b contract.AccountingEntry) (contract.AccountingEntry, error) {
	copyA, _, err := copyEntry(a)
	if err != nil {
		return a, err
	}
	a = copyA
	if a.Tokens != nil {
		if b.Tokens == nil {
			return a, fmt.Errorf("incompatible token bucket")
		}
		left := []*contract.Measure[contract.Count]{&a.Tokens.Total, &a.Tokens.Input, &a.Tokens.Output, &a.Tokens.CacheRead, &a.Tokens.CacheWrite, &a.Tokens.Reasoning}
		right := []contract.Measure[contract.Count]{b.Tokens.Total, b.Tokens.Input, b.Tokens.Output, b.Tokens.CacheRead, b.Tokens.CacheWrite, b.Tokens.Reasoning}
		for i, m := range left {
			*m, err = addMeasure(*m, right[i], AddCount)
			if err != nil {
				return a, err
			}
		}
	}
	if len(a.Credits) != len(b.Credits) || len(a.Costs) != len(b.Costs) {
		return a, fmt.Errorf("incompatible economic bucket")
	}
	for i := range a.Credits {
		a.Credits[i].Amount, err = addMeasure(a.Credits[i].Amount, b.Credits[i].Amount, AddAmount)
		if err != nil {
			return a, err
		}
	}
	for i := range a.Costs {
		a.Costs[i].Amount, err = addMeasure(a.Costs[i].Amount, b.Costs[i].Amount, AddAmount)
		if err != nil {
			return a, err
		}
	}
	return a, nil
}
