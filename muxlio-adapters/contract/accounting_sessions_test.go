package contract

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAccountingSessionsAndBoundedProvenanceRoundTrip(t *testing.T) {
	a := Accounting{Revision: 1, Sessions: []AccountingSession{{ID: "owned", Provider: "fixture", Source: "fixture-init", Version: "1.2.3", Principal: "user:fixture", StartedAt: "2026-11-12T10:00:00Z", FinishedAt: "2026-11-12T10:01:00Z", Isolated: true}}, Entries: []AccountingEntry{{ID: "quote", Costs: []CostMeasure{{Currency: "USD", Basis: "provider-metered", Amount: Measure[Amount]{Collected: &Reading[Amount]{Value: "0", Source: "fixture-ledger", Fidelity: "metered", Attributes: map[string]string{"state": "provisional", "included-fee-usd": "0"}}}}}}}}
	raw, err := CanonicalAccounting(a)
	if err != nil {
		t.Fatal(err)
	}
	var copy Accounting
	if err := json.Unmarshal(raw, &copy); err != nil {
		t.Fatal(err)
	}
	after, err := CanonicalAccounting(copy)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("session/provenance changed on roundtrip", err)
	}
	for _, change := range []func(*Accounting){
		func(a *Accounting) { a.Sessions[0].ID = "bad\nidentity" },
		func(a *Accounting) { a.Sessions[0].FinishedAt = "2026-11-11T10:00:00Z" },
		func(a *Accounting) { a.Sessions[0].StartedAt = "" },
		func(a *Accounting) {
			a.Entries[0].Costs[0].Amount.Collected.Attributes["state"] = strings.Repeat("x", 257)
		},
	} {
		var broken Accounting
		_ = json.Unmarshal(raw, &broken)
		change(&broken)
		if broken.Validate() == nil {
			t.Fatal("invalid session or provenance accepted")
		}
	}
}
