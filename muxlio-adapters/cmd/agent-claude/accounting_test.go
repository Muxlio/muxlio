package main

import (
	"encoding/json"
	"testing"
)

func rawClaudeEvent(t *testing.T, source string) map[string]json.RawMessage {
	t.Helper()
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(source), &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestClaudeAccountingModelUsageIncludesSubagents(t *testing.T) {
	entry, err := claudeAccountingEntry(rawClaudeEvent(t, `{
		"type":"result",
		"usage":{"input_tokens":2,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},
		"modelUsage":{
			"model-a":{"inputTokens":10,"outputTokens":20,"cacheReadInputTokens":30,"cacheCreationInputTokens":40,"thinkingTokens":4},
			"model-b":{"inputTokens":5,"outputTokens":7,"cacheReadInputTokens":2,"cacheCreationInputTokens":3,"thinkingTokens":1}
		},
		"total_cost_usd":1.23e-7
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Tokens == nil || entry.Tokens.Input.Collected.Value != "90" || entry.Tokens.Output.Collected.Value != "27" || entry.Tokens.Total.Collected.Value != "117" || entry.Tokens.CacheRead.Collected.Value != "32" || entry.Tokens.CacheWrite.Collected.Value != "43" || entry.Tokens.Reasoning.Collected.Value != "5" {
		t.Fatalf("wrong tokens: %+v", entry.Tokens)
	}
	if len(entry.Costs) != 1 || entry.Costs[0].Amount.Collected.Value != "0.000000123" {
		t.Fatalf("wrong cost: %+v", entry.Costs)
	}
}

func TestClaudeAccountingExactFallbackFields(t *testing.T) {
	entry, err := claudeAccountingEntry(rawClaudeEvent(t, `{
		"type":"result",
		"usage":{"input_tokens":5,"output_tokens":7,"cache_read_input_tokens":0,"cache_creation_input_tokens":9},
		"totalCostUsd":999
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Tokens.Input.Collected.Value != "14" || entry.Tokens.CacheRead.Collected.Value != "0" || entry.Tokens.CacheWrite.Collected.Value != "9" {
		t.Fatalf("wrong main-loop counts: %+v", entry.Tokens)
	}
	if len(entry.Costs) != 0 {
		t.Fatal("accepted an undocumented guessed cost field")
	}
}

func TestClaudeAccountingPartialUsageRemainsUnknown(t *testing.T) {
	entry, err := claudeAccountingEntry(rawClaudeEvent(t, `{
		"type":"result",
		"usage":{"input_tokens":12,"output_tokens":8,"cache_read_input_tokens":null,"cache_creation_input_tokens":5},
		"total_cost_usd":0
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if entry.Tokens == nil || entry.Tokens.Input.Collected != nil || entry.Tokens.Total.Collected != nil || entry.Tokens.Output.Collected.Value != "8" {
		t.Fatalf("missing cache field should not yield complete input: %+v", entry.Tokens)
	}
	if entry.Costs[0].Amount.Collected.Value != "0" {
		t.Fatal("reported zero must remain zero")
	}
}

func TestClaudeAccountingRejectsNegativeAndInvalidPrecision(t *testing.T) {
	cases := []string{
		`{"type":"result","usage":{"cache_creation_input_tokens":-2}}`,
		`{"type":"result","total_cost_usd":-1.0}`,
		`{"type":"result","total_cost_usd":1e-19}`,
		`{"type":"result","modelUsage":{"a":{"inputTokens":1,"outputTokens":2,"cacheReadInputTokens":-3,"cacheCreationInputTokens":4}}}`,
	}
	for _, tc := range cases {
		_, err := claudeAccountingEntry(rawClaudeEvent(t, tc))
		if err == nil {
			t.Fatalf("expected invalid measurement: %s", tc)
		}
	}
}

func TestClaudeAccountingMissingCostIsUnknown(t *testing.T) {
	entry, err := claudeAccountingEntry(rawClaudeEvent(t, `{"type":"result","total_cost_usd":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Costs) > 0 {
		t.Fatalf("null cost became a numeric reading: %+v", entry.Costs)
	}
}
