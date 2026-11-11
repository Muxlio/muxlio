package state

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"testing"
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

	must(s.RecordExecutionAttempt(ExecutionAttempt{ID: "b", RunID: "before", State: "succeeded", Accounting: testaccounting.WithCosts(nil, "0.25", "")}))
	must(s.RecordExecutionAttempt(ExecutionAttempt{ID: "a", RunID: "after", State: "succeeded", Accounting: testaccounting.WithCosts(testaccounting.Tokens(0, 0, 0), "0", "")}))
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
	if len(comparison.BeforeAccounting.Costs) != 1 || *comparison.BeforeAccounting.Costs[0].Value.KnownSubtotal != "0.25" || len(comparison.AfterAccounting.Costs) != 1 || *comparison.AfterAccounting.Costs[0].Value.KnownSubtotal != "0" || comparison.AfterAccounting.Costs[0].Value.EffectiveTotal != nil {
		t.Fatalf("accounting comparison: %+v", comparison)
	}
	if len(comparison.ReusedOutputs) != 1 {
		t.Fatal("reuse provenance missing")
	}
}
