package state

import (
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"testing"
)

func TestAccountingSummaryMapsMissingZeroPendingAndFailedAttempts(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		missing     bool
		want        string
		complete    bool
	}{{"zero", "succeeded", false, "0", true}, {"failed consumption", "failed", false, "0", true}, {"missing", "succeeded", true, "", false}, {"pending", "running", false, "0", false}} {
		t.Run(tc.name, func(t *testing.T) {
			a := ExecutionAttempt{State: tc.state}
			if !tc.missing {
				a.Accounting = testaccounting.Tokens(0, 0, 0)
			}
			s, err := SummarizeAccounting([]ExecutionAttempt{a})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if s.Tokens.KnownSubtotal != nil {
					t.Fatal("missing became zero")
				}
			} else if s.Tokens.KnownSubtotal == nil || *s.Tokens.KnownSubtotal != tc.want {
				t.Fatalf("summary=%+v", s)
			}
			if (s.Tokens.EffectiveTotal != nil) != tc.complete {
				t.Fatalf("false completeness: %+v", s.Tokens)
			}
		})
	}
}
func TestAccountingSummaryDoesNotAddAlternativeCosts(t *testing.T) {
	s, err := SummarizeAccounting([]ExecutionAttempt{{State: "succeeded", Accounting: testaccounting.WithCosts(testaccounting.Tokens(100, 20, 30), "0.18065595854063016", "0.2")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Costs) != 1 || *s.Costs[0].Value.KnownSubtotal != "0.18065595854063016" || s.Costs[0].Value.EstimatedFallbackSubtotal != nil || *s.Tokens.KnownSubtotal != "120" {
		t.Fatalf("summary=%+v", s)
	}
}

func TestMemoryAccountingSnapshotsDoNotAliasCallers(t *testing.T) {
	s := New()
	a := testaccounting.Tokens(10, 2, 0)
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "a", RunID: "r", Accounting: a}); err != nil {
		t.Fatal(err)
	}
	a.Entries[0].Tokens.Input.Collected.Value = "999"
	stored, err := s.ListExecutionAttempts("r", "")
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].Accounting.Entries[0].Tokens.Input.Collected.Value != "10" {
		t.Fatal("record retained caller memory")
	}
	stored[0].Accounting.Entries[0].Tokens.Input.Collected.Value = "888"
	a = testaccounting.Tokens(20, 2, 0)
	a.Revision = 2
	a.Final = false
	// Use a separate active attempt so finality does not obscure isolation.
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "b", RunID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAccountingSnapshot("b", *a); err != nil {
		t.Fatal(err)
	}
	a.Entries[0].Tokens.Input.Collected.Value = "777"
	stored, err = s.ListExecutionAttempts("r", "")
	if err != nil {
		t.Fatal(err)
	}
	if stored[0].Accounting.Entries[0].Tokens.Input.Collected.Value != "10" || stored[1].Accounting.Entries[0].Tokens.Input.Collected.Value != "20" {
		t.Fatal("snapshot or read mutated durable accounting")
	}
}
