package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCodexLiveFixturePreservesReportedSubsetsAndZero(t *testing.T) {
	raw, err := os.ReadFile("testdata/0.167.0-accounting.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]json.RawMessage
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	m, err := codexTokenMeasures(event["usage"])
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.Total.Collected.Value != "14892" || m.CacheRead.Collected.Value != "12288" || m.CacheWrite.Collected == nil || m.CacheWrite.Collected.Value != "0" || m.Reasoning.Collected == nil || m.Reasoning.Collected.Value != "0" {
		t.Fatalf("live reported dimensions lost: %+v", m)
	}
	m, err = codexTokenMeasures(json.RawMessage(`{"input_tokens":100,"output_tokens":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.CacheWrite.Collected != nil || m.Reasoning.Collected != nil {
		t.Fatal("omitted subsets became zero")
	}
}
