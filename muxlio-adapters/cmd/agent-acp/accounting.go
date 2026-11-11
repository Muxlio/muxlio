package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/acp"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

type acpAccounting struct {
	session, authority string
	started            bool
	counters           *accounting.AmountCounterTracker
	recorder           *accounting.Recorder
	publisher          *adapter.AccountingPublisher
}

func newACPAccounting(authority string) *acpAccounting {
	return &acpAccounting{authority: authority, counters: accounting.NewAmountCounterTracker(), recorder: accounting.NewRecorder(), publisher: adapter.NewAccountingPublisher()}
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

func (a *acpAccounting) observe(raw json.RawMessage) {
	for _, model := range acp.SelectedModels(raw) {
		_ = a.recorder.ReportModel(contract.AccountingModel{ID: model, Role: "selected", Source: "acp-session-config"})
	}
	var v struct {
		Kind string `json:"sessionUpdate"`
		Cost *struct {
			Amount   json.RawMessage `json:"amount"`
			Currency string          `json:"currency"`
		} `json:"cost"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Kind != "usage_update" || v.Cost == nil || a.session == "" {
		return
	}
	amount, err := accounting.ParseJSONAmount(v.Cost.Amount)
	if err != nil {
		adapter.AppendLogChunk("stderr", "accounting-observation-rejected")
		return
	}
	key := contract.AccountingKey{Kind: "credits", Provider: a.authority, Unit: v.Cost.Currency}
	if currencyCode.MatchString(v.Cost.Currency) {
		key = contract.AccountingKey{Kind: "cost", Currency: v.Cost.Currency, Basis: "unspecified"}
	}
	if err := (&contract.Accounting{Revision: 1, Coverage: []contract.MeterCoverage{{Key: key, State: "partial"}}}).Validate(); err != nil {
		return
	}
	scope := fmt.Sprintf("session-meter-%x", sha256.Sum256([]byte(v.Cost.Currency)))
	if !a.started {
		_ = a.counters.Baseline(scope, a.session, amount)
		return
	}
	delta, owned, err := a.counters.SinceBaseline(scope, a.session, amount)
	if err != nil {
		_ = a.recorder.SetCoverage(key, "partial", "cumulative-counter-reset")
		adapter.AppendLogChunk("stderr", "accounting-cumulative-counter-reset")
		a.publish()
		return
	}
	if !owned {
		return
	} // No pre-execution baseline: session cost remains diagnostic.
	m := contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: delta, Source: "acp-session-meter", Fidelity: "derived-exact"}}
	entry := contract.AccountingEntry{ID: scope}
	if key.Kind == "cost" {
		entry.Costs = []contract.CostMeasure{{Currency: key.Currency, Basis: key.Basis, Amount: m}}
	} else {
		entry.Credits = []contract.CreditMeasure{{Provider: key.Provider, Unit: key.Unit, Amount: m}}
	}
	if err := a.recorder.ObserveCumulative(accounting.Part{Scope: scope, Entry: entry}); err != nil {
		adapter.AppendLogChunk("stderr", "accounting-observation-rejected")
		return
	}
	_ = a.recorder.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "unknown", "context-is-not-consumption")
	_ = a.recorder.SetCoverage(key, "partial", "source-coverage-unverified")
	a.publish()
}
func (a *acpAccounting) publish() {
	if s, err := a.recorder.Snapshot(); err == nil && (len(s.Entries) > 0 || len(s.Models) > 0) {
		_ = a.publisher.Publish(*s, false)
	}
}
func (a *acpAccounting) finish() *contract.Accounting {
	if s, err := a.recorder.Snapshot(); err == nil && len(s.Entries) == 0 && len(s.Models) == 0 {
		_ = a.publisher.Close()
		return nil
	}
	s, err := adapter.FinalizeAccounting(a.recorder, a.publisher)
	if err != nil || s == nil || (len(s.Entries) == 0 && len(s.Models) == 0) {
		return nil
	}
	return s
}
