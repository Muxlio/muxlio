package main

import (
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func TestMergeTerminalUsageCompleteReplacesPrior(t *testing.T) {
	prior := adapter.ParseTokenUsage(json.RawMessage(`{"input_tokens":10302,"output_tokens":582,"cache_read_tokens":8113}`))
	terminal := adapter.ParseTokenUsage(json.RawMessage(`{"input_tokens":10418,"output_tokens":589,"cache_read_tokens":8113}`))
	got := mergeTerminalUsage(prior, terminal)
	if got == nil || got.Input != 10418 || got.Output != 589 || got.Cached != 8113 {
		t.Fatalf("mergeTerminalUsage() = %+v", got)
	}
}

func TestAccumulateStepUsageSumsDistinctSteps(t *testing.T) {
	var tracker stepUsageTracker
	total, err := accumulateStepUsage(nil, &tracker, "c1", 3, "DONE",
		json.RawMessage(`{"input_tokens":10302,"output_tokens":582,"cache_read_tokens":8113}`))
	if err != nil {
		t.Fatal(err)
	}
	total, err = accumulateStepUsage(total, &tracker, "c1", 4, "DONE",
		json.RawMessage(`{"input_tokens":116,"output_tokens":7,"cache_read_tokens":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if total == nil || total.Input != 10418 || total.Output != 589 || total.Cached != 8113 {
		t.Fatalf("aggregated step usage = %+v", total)
	}
}

func TestAccumulateStepUsageDedupesRepeatedDone(t *testing.T) {
	var tracker stepUsageTracker
	usageRaw := json.RawMessage(`{"input_tokens":116,"output_tokens":7}`)
	total, err := accumulateStepUsage(nil, &tracker, "c1", 2, "ACTIVE", usageRaw)
	if err != nil || total != nil {
		t.Fatalf("ACTIVE should not aggregate, total=%+v err=%v", total, err)
	}
	total, err = accumulateStepUsage(total, &tracker, "c1", 2, "DONE", usageRaw)
	if err != nil {
		t.Fatal(err)
	}
	total, err = accumulateStepUsage(total, &tracker, "c1", 2, "DONE", usageRaw)
	if err != nil {
		t.Fatal(err)
	}
	if total == nil || total.Input != 116 || total.Output != 7 {
		t.Fatalf("deduped step usage = %+v", total)
	}
}
