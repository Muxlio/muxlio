package main

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/adapter"
	"os"
	"testing"
)

func TestCursorLiveFixturePreservesCacheWriteZero(t *testing.T) {
	raw, err := os.ReadFile("testdata/2026.11.05-86047df-accounting.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]json.RawMessage
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	usage := parseCursorTokenUsage(event["usage"])
	m := adapter.CollectedTokenMeasures(usage, "cursor-stream")
	if m == nil || m.Total.Collected.Value != "10604" || m.Input.Collected.Value != "10563" || m.CacheRead.Collected.Value != "576" || m.CacheWrite.Collected == nil || m.CacheWrite.Collected.Value != "0" {
		t.Fatalf("live dimensions lost: %+v", m)
	}
}
