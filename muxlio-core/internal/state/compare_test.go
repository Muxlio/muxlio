package state

import (
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestCompareRunsScopesResultsAndKnownCosts(t *testing.T) {
	s := New()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []*Run{
		{ID: "before", Input: map[string]any{"n": 1}, PlanDigest: "v1", PlanJSON: json.RawMessage(`{"name":"v1"}`)},
		{ID: "after", Input: map[string]any{"n": 2}, PlanDigest: "v2", PlanJSON: json.RawMessage(`{"name":"v2"}`)},
	} {
		must(s.CreateRun(r))
		must(s.SetRunState(r.ID, "succeeded"))
	}
	for _, a := range []*Activation{
		{ID: "b1", RunID: "before", ScopePath: []string{"map:a"}, NodeID: "work", State: "queued", Inputs: json.RawMessage(`{"x":1}`)},
		{ID: "a1", RunID: "after", ScopePath: []string{"map:a"}, NodeID: "work", State: "queued", Inputs: json.RawMessage(`{"x":2}`)},
		{ID: "a2", RunID: "after", ScopePath: []string{"map:b"}, NodeID: "work", State: "queued"},
	} {
		must(s.CreateActivation(a))
		must(s.SetActivationOutputs(a.ID, map[string]any{"result": a.ID}))
	}
	cost := 0.25
	zero := 0.0
	must(s.RecordExecutionAttempt(ExecutionAttempt{ID: "b", RunID: "before", State: "succeeded", Measurements: &contract.Measurements{CostUSD: &cost}}))
	must(s.RecordExecutionAttempt(ExecutionAttempt{ID: "a", RunID: "after", State: "succeeded", Usage: &TokenUsage{}, Measurements: &contract.Measurements{CostUSD: &zero}}))
	must(s.RecordExecutionAttempt(ExecutionAttempt{ID: "unknown", RunID: "after", State: "succeeded"}))
	must(s.AppendEvent("after", "activation.output_reused", "a1", map[string]any{"sourceActivationId": "b1", "fingerprint": "sha256:test"}))
	comparison, err := CompareRuns(s, "before", "after")
	must(err)
	if !comparison.InputsChanged || !comparison.PlanChanged || len(comparison.BeforePlan) == 0 || len(comparison.AfterPlan) == 0 {
		t.Fatalf("run differences: %+v", comparison)
	}
	if len(comparison.Activations) != 2 || !comparison.Activations[0].Changed || !comparison.Activations[1].Changed || comparison.Activations[0].Before == nil || comparison.Activations[1].Before != nil {
		t.Fatalf("scope alignment: %+v", comparison.Activations)
	}
	if comparison.BeforeUsage.CostUSD.Observed == nil || *comparison.BeforeUsage.CostUSD.Observed != cost || comparison.AfterUsage.CostUSD.Observed == nil || *comparison.AfterUsage.CostUSD.Observed != 0 || comparison.AfterUsage.CostUSD.Unknown != 1 || comparison.BeforeUsage.Tokens.Observed != nil || comparison.AfterUsage.Tokens.Observed == nil || *comparison.AfterUsage.Tokens.Observed != 0 {
		t.Fatalf("usage: %+v %+v", comparison.BeforeUsage, comparison.AfterUsage)
	}
	if len(comparison.ReusedOutputs) != 1 {
		t.Fatal("reuse provenance missing")
	}
}
