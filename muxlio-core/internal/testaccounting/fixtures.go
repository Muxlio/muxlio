// Package testaccounting contains provider-neutral fixtures used by tests.
package testaccounting

import (
	"bytes"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"strconv"
)

func Tokens(input, output, cache int64) *contract.Accounting {
	m := func(v int64) contract.Measure[contract.Count] {
		return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{Value: contract.Count(fmt.Sprint(v)), Source: "fixture", Fidelity: "metered"}}
	}
	return &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call", Tokens: &contract.TokenMeasures{Input: m(input), Output: m(output), CacheRead: m(cache)}}}, Coverage: []contract.MeterCoverage{{Key: contract.AccountingKey{Kind: "tokens"}, State: "complete"}}}
}
func TokenValue(a *contract.Accounting, dimension string) int64 {
	if a == nil || len(a.Entries) == 0 || a.Entries[0].Tokens == nil {
		return -1
	}
	t := a.Entries[0].Tokens
	var m contract.Measure[contract.Count]
	switch dimension {
	case "input":
		m = t.Input
	case "output":
		m = t.Output
	case "cached":
		m = t.CacheRead
	}
	if m.Collected == nil {
		return -1
	}
	n, err := strconv.ParseInt(string(m.Collected.Value), 10, 64)
	if err != nil {
		panic(err)
	}
	return n
}
func Equal(a, b *contract.Accounting) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	x, e := contract.CanonicalAccounting(*a)
	y, f := contract.CanonicalAccounting(*b)
	return e == nil && f == nil && bytes.Equal(x, y)
}
func WithCosts(a *contract.Accounting, collected, estimated string) *contract.Accounting {
	if a == nil {
		a = &contract.Accounting{Revision: 1, Final: true, Entries: []contract.AccountingEntry{{ID: "call"}}}
	}
	m := contract.Measure[contract.Amount]{}
	if collected != "" {
		m.Collected = &contract.Reading[contract.Amount]{Value: contract.Amount(collected), Source: "fixture", Fidelity: "metered"}
	}
	if estimated != "" {
		m.Estimated = &contract.Reading[contract.Amount]{Value: contract.Amount(estimated), Source: "fixture", Fidelity: "derived-exact", Method: "fixture", Version: "1"}
	}
	a.Entries[0].Costs = []contract.CostMeasure{{Currency: "USD", Basis: "api-equivalent", Amount: m}}
	a.Coverage = append(a.Coverage, contract.MeterCoverage{Key: contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: "api-equivalent"}, State: "complete"})
	return a
}
