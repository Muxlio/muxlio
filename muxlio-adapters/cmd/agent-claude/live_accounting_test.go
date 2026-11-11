package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestClaudeLiveAuthenticationFailureRetainsExplicitZero(t *testing.T) {
	raw, err := os.ReadFile("testdata/2.1.316-auth-failure.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]json.RawMessage
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	entry, err := claudeAccountingEntry(event)
	if err != nil {
		t.Fatal(err)
	}
	if string(event["is_error"]) != "true" || entry.Tokens == nil || entry.Tokens.Total.Collected.Value != "0" || entry.Tokens.Reasoning.Collected == nil || entry.Tokens.Reasoning.Collected.Value != "0" || len(entry.Costs) != 1 || entry.Costs[0].Amount.Collected.Value != "0" {
		t.Fatalf("failed request zeroes lost: %+v", entry)
	}
}
