package main

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
	"testing"
	"time"
)

func TestKiroCreditReplacementGatesAndReplay(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, context, turn, usage, want string
		at                               time.Time
	}{
		{"precise", "commercial-paid-usd", "one", "0.18065595854063016", "0.007226238341625206", at},
		{"zero", "commercial-paid-usd", "one", "0", "0", at},
		{"unknown-plan", "", "one", "1", "", at},
		{"govcloud", "govcloud", "one", "1", "", at},
		{"unowned", "commercial-paid-usd", "", "1", "", at},
		{"historic", "commercial-paid-usd", "one", "1", "", at.AddDate(0, 0, -1)},
		{"below-precision", "commercial-paid-usd", "one", "0.000000000000000001", "", at},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"kind":"turn_completion","turnId":"` + tc.turn + `","promptTurnSummaries":[{"unit":"credit","usage":` + tc.usage + `}]}`)
			r := accounting.NewRecorder()
			for i := 0; i < 2; i++ {
				if err := recordKiroCreditsAt(r, raw, tc.context, tc.at); err != nil {
					t.Fatal(err)
				}
			}
			a, err := r.Finish()
			if err != nil {
				t.Fatal(err)
			}
			s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "failed", Accounting: a}})
			if err != nil {
				t.Fatal(err)
			}
			if s.Credits[0].Value.KnownSubtotal == nil || *s.Credits[0].Value.KnownSubtotal != tc.usage {
				t.Fatal("collected credits lost or replay counted twice")
			}
			v := s.ReferenceValue
			if tc.want == "" {
				if v.KnownSubtotal != nil {
					t.Fatal("ineligible credits priced")
				}
				return
			}
			if v.KnownSubtotal == nil || *v.KnownSubtotal != tc.want || v.EffectiveTotal != nil {
				t.Fatalf("incorrect precise partial value: %+v", v)
			}
			c := a.Entries[0].Costs[0]
			if c.Amount.Collected != nil || c.Amount.Estimated.CatalogID == "" || c.Amount.Estimated.Method != "credit-replacement" {
				t.Fatal("lost distinction or provenance")
			}
			if err := contract.Amount(*v.KnownSubtotal).Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
