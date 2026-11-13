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
			if err := r.Replace(mustAntigravityTokenCounters(t, event.Result.Usage)); err != nil {
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

// Captured from agy 1.5.7 using a short terminal/write/read task. Native input
// and total exclude cache reads, including on the final cumulative result.
func TestLiveCachedUsageKeepsStreamingAndTerminalConsumption(t *testing.T) {
	raw, err := os.ReadFile("testdata/1.5.7-cached-success.jsonl")
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
				t.Fatalf("live step %v rejected: %v", *event.Step.StepIndex, err)
			}
		}
		if event.Event == "result" {
			if err := r.Replace(mustAntigravityTokenCounters(t, event.Result.Usage)); err != nil {
				t.Fatalf("terminal usage rejected: %v", err)
			}
		}
	}
	a := r.Finish()
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]struct {
		got  *string
		want string
	}{
		"input":     {s.TokenDimensions.Input.KnownSubtotal, "34255"},
		"output":    {s.TokenDimensions.Output.KnownSubtotal, "643"},
		"cache":     {s.TokenDimensions.CacheRead.KnownSubtotal, "19314"},
		"reasoning": {s.TokenDimensions.Reasoning.KnownSubtotal, "474"},
		"total":     {s.Tokens.KnownSubtotal, "34898"},
	} {
		if check.got == nil || *check.got != check.want {
			t.Errorf("%s = %v, want %s", name, check.got, check.want)
		}
	}
}
