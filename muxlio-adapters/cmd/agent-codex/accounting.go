package main

import (
	"encoding/json"
	"fmt"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

// Codex 0.167.0 turn.completed reports inclusive input/output counters for
// the fresh ephemeral thread. Cache writes and reasoning are subsets.
func codexTokenMeasures(raw json.RawMessage) (*contract.TokenMeasures, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var v struct {
		Input     *int64 `json:"input_tokens"`
		Output    *int64 `json:"output_tokens"`
		Read      *int64 `json:"cached_input_tokens"`
		Write     *int64 `json:"cache_write_input_tokens"`
		Reasoning *int64 `json:"reasoning_output_tokens"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("invalid codex usage")
	}
	t := &contract.TokenMeasures{}
	for _, field := range []struct {
		n *int64
		m *contract.Measure[contract.Count]
	}{{v.Input, &t.Input}, {v.Output, &t.Output}, {v.Read, &t.CacheRead}, {v.Write, &t.CacheWrite}, {v.Reasoning, &t.Reasoning}} {
		if field.n == nil {
			continue
		}
		if *field.n < 0 {
			return nil, fmt.Errorf("negative codex usage")
		}
		field.m.Collected = &contract.Reading[contract.Count]{Value: contract.Count(fmt.Sprint(*field.n)), Source: "codex-turn", Fidelity: "metered"}
	}
	if v.Input == nil && v.Output == nil && v.Read == nil && v.Write == nil && v.Reasoning == nil {
		return nil, nil
	}
	if v.Input != nil && v.Output != nil {
		total, err := accounting.AddCount(contract.Count(fmt.Sprint(*v.Input)), contract.Count(fmt.Sprint(*v.Output)))
		if err != nil {
			return nil, err
		}
		t.Total.Collected = &contract.Reading[contract.Count]{Value: total, Source: "codex-turn", Fidelity: "derived-exact"}
	}
	if err := (&contract.Accounting{Revision: 1, Entries: []contract.AccountingEntry{{ID: "thread", Tokens: t}}}).Validate(); err != nil {
		return nil, err
	}
	return t, nil
}
