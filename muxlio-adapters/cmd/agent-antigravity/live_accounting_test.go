package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/adapter"
)

func TestLiveStepAndTerminalUsageReplaceTheSameScope(t *testing.T) {
	raw, err := os.ReadFile("testdata/1.5.5-success.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	r := adapter.NewTokenRecorder("antigravity-stream")
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var event struct {
			Event  string          `json:"event"`
			Step   stepUsageUpdate `json:"step_update"`
			Result struct {
				Usage json.RawMessage `json:"usage"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "step_update" {
			if err := observeStepUsage(r, event.Step); err != nil {
				t.Fatal(err)
			}
		}
		if event.Event == "result" {
			if err := r.Replace(antigravityTokenCounters(event.Result.Usage)); err != nil {
				t.Fatal(err)
			}
		}
	}
	a := r.Finish()
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil || s.Tokens.KnownSubtotal == nil || *s.Tokens.KnownSubtotal != "9051" || s.TokenDimensions.Reasoning.KnownSubtotal == nil || *s.TokenDimensions.Reasoning.KnownSubtotal != "58" || s.TokenDimensions.CacheRead.KnownSubtotal == nil || *s.TokenDimensions.CacheRead.KnownSubtotal != "0" {
		t.Fatalf("live totals duplicated or lost: %+v %v", s, err)
	}
}
