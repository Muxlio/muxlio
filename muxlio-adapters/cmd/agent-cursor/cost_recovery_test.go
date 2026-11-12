package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

func cursorBillingFixture() (cursorBillingConfig, contract.AccountingSession) {
	return cursorBillingConfig{VerifiedJoinVersion: "test-1", Email: "user@example.test"}, contract.AccountingSession{ID: "owned", Provider: "cursor", Source: "cursor-init", Version: "test-1", Principal: "user:user@example.test", Isolated: true, StartedAt: "1970-01-01T00:00:01Z", FinishedAt: "1970-01-01T00:00:02Z"}
}

const cursorLedgerEvent = `{"timestamp":"1500","conversationId":"owned","userEmail":"user@example.test","model":"routed-model","kind":"Included in Business","isChargeable":false,"isTokenBasedCall":true,"isHeadless":true,"chargedCents":21.36232,"cursorTokenFee":1.18,"tokenUsage":{"totalCents":20.18232,"discountPercentOff":10}}`

func ledgerResponse(events string) string {
	return `{"totalUsageEventsCount":1,"pagination":{"currentPage":1,"numPages":1,"pageSize":1000,"hasNextPage":false},"usageEvents":[` + events + `]}`
}

func TestCursorDirectCostsKeepBasesAndIncludedFeesSeparate(t *testing.T) {
	cfg, session := cursorBillingFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "secret" || password != "" || r.Method != "POST" {
			t.Error("incorrect billing authentication")
		}
		var q map[string]any
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q["email"] != cfg.Email || q["startDate"] != float64(1000) || q["endDate"] != float64(2000) {
			t.Error("incorrect principal/window filters")
		}
		_, _ = w.Write([]byte(ledgerResponse(cursorLedgerEvent)))
	}))
	defer server.Close()
	parts, err := fetchCursorWindow(context.Background(), server.Client(), server.URL, "secret", cfg, session)
	if err != nil || len(parts) != 1 {
		t.Fatalf("%+v %v", parts, err)
	}
	r := accounting.NewRecorder()
	if err := r.ReplaceScope(cursorLedgerScope, parts); err != nil {
		t.Fatal(err)
	}
	for _, basis := range []string{"provider-metered", "model-reference"} {
		_ = r.SetCoverage(contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: basis}, "partial", "provider-window-provisional")
	}
	a, _ := r.Finish()
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil || *s.ReferenceValue.KnownSubtotal != "0.2136232" || s.ReferenceValue.EffectiveTotal != nil || s.ReferenceValue.Methods[0] != "provider-metered" {
		t.Fatalf("fee/model total double counted: %+v %v", s.ReferenceValue, err)
	}
	if parts[0].Entry.Tokens != nil || len(parts[0].Entry.Costs) != 2 {
		t.Fatal("ledger invented token usage or lost economic basis")
	}
	charge := parts[0].Entry.Costs[0].Amount.Collected
	if charge.Fidelity != "metered" || charge.Attributes["included-fee-usd"] != "0.0118" || charge.Attributes["is-chargeable"] != "false" || charge.Attributes["discount-percent-off"] != "10" {
		t.Fatalf("lost classification: %+v", charge)
	}
}

func TestCursorLedgerOwnershipAndFailureGates(t *testing.T) {
	for _, tc := range []struct {
		name, body, key string
		mutate          func(*cursorBillingConfig, *contract.AccountingSession)
		status          int
	}{
		{name: "missing-key", body: ledgerResponse(cursorLedgerEvent)},
		{name: "unverified-version", body: ledgerResponse(cursorLedgerEvent), key: "key", mutate: func(c *cursorBillingConfig, s *contract.AccountingSession) { c.VerifiedJoinVersion = "old" }},
		{name: "shared-conversation", body: ledgerResponse(cursorLedgerEvent), key: "key", mutate: func(c *cursorBillingConfig, s *contract.AccountingSession) { s.Isolated = false }},
		{name: "wrong-principal", body: ledgerResponse(strings.ReplaceAll(cursorLedgerEvent, "user@example.test", "other@example.test")), key: "key"},
		{name: "no-conversation", body: ledgerResponse(strings.ReplaceAll(cursorLedgerEvent, `"owned"`, `""`)), key: "key"},
		{name: "other-conversation", body: ledgerResponse(strings.ReplaceAll(cursorLedgerEvent, `"owned"`, `"other"`)), key: "key"},
		{name: "missing-page", body: strings.Replace(ledgerResponse(cursorLedgerEvent), `"totalUsageEventsCount":1`, `"totalUsageEventsCount":2`, 1), key: "key"},
		{name: "ambiguous-events", body: strings.Replace(ledgerResponse(cursorLedgerEvent+","+cursorLedgerEvent), `"totalUsageEventsCount":1`, `"totalUsageEventsCount":2`, 1), key: "key"},
		{name: "auth", status: 403, key: "key"},
		{name: "schema", body: `{"unexpected":true}`, key: "key"},
		{name: "negative", body: ledgerResponse(strings.Replace(cursorLedgerEvent, "21.36232", "-1", 1)), key: "key"},
		{name: "out-of-window", body: ledgerResponse(strings.Replace(cursorLedgerEvent, `"1500"`, `"2500"`, 1)), key: "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, session := cursorBillingFixture()
			if tc.mutate != nil {
				tc.mutate(&cfg, &session)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			parts, err := fetchCursorWindow(context.Background(), server.Client(), server.URL, tc.key, cfg, session)
			if err == nil || len(parts) > 0 {
				t.Fatalf("ineligible evidence accepted: %+v %v", parts, err)
			}
		})
	}
}

func TestCursorRevisedWindowReplacesPreviousAmount(t *testing.T) {
	cfg, session := cursorBillingFixture()
	body := ledgerResponse(cursorLedgerEvent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	initial := contract.Accounting{Revision: 7, Final: true, Sessions: []contract.AccountingSession{session}}
	first, err := reconcileCursorSnapshot(context.Background(), server.Client(), server.URL, "key", cfg, initial)
	if err != nil {
		t.Fatal(err)
	}
	body = ledgerResponse(strings.Replace(cursorLedgerEvent, "21.36232", "42", 1))
	second, err := reconcileCursorSnapshot(context.Background(), server.Client(), server.URL, "key", cfg, *first)
	if err != nil {
		t.Fatal(err)
	}
	third, err := reconcileCursorSnapshot(context.Background(), server.Client(), server.URL, "key", cfg, *second)
	if err != nil {
		t.Fatal(err)
	}
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: third}})
	if err != nil || *s.ReferenceValue.KnownSubtotal != "0.42" || third.Revision != 10 || len(third.Entries) != 1 || len(initial.Entries) != 0 {
		t.Fatalf("revision added or mutated old amount: %+v %v", s.ReferenceValue, err)
	}
	// A failed refresh preserves confirmed consumption and never becomes zero.
	body = `{}`
	failed, err := reconcileCursorSnapshot(context.Background(), server.Client(), server.URL, "key", cfg, *third)
	if err != nil {
		t.Fatal(err)
	}
	s, err = accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: failed}})
	if err != nil || *s.ReferenceValue.KnownSubtotal != "0.42" || s.ReferenceValue.EffectiveTotal != nil {
		t.Fatal("failed refresh discarded confirmed usage")
	}
}

func TestCursorCentsAndRequestBasedZero(t *testing.T) {
	for raw, want := range map[string]string{"21.36232": "0.2136232", "0": "0", "1e-7": "0.000000001", "125": "1.25"} {
		got, err := cursorCentsUSD(json.RawMessage(raw))
		if err != nil || got == nil || string(*got) != want {
			t.Fatalf("%s: %v %v", raw, got, err)
		}
	}
	if got, err := cursorCentsUSD(nil); err != nil || got != nil {
		t.Fatal("missing cents became zero")
	}
	cfg, session := cursorBillingFixture()
	event := `{"timestamp":"1500","conversationId":"owned","userEmail":"user@example.test","kind":"Included","isChargeable":false,"isTokenBasedCall":false,"isHeadless":true,"chargedCents":0}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(ledgerResponse(event))) }))
	defer server.Close()
	parts, err := fetchCursorWindow(context.Background(), server.Client(), server.URL, "key", cfg, session)
	if err != nil || len(parts) != 1 || len(parts[0].Entry.Costs) != 1 || parts[0].Entry.Costs[0].Amount.Collected.Value != "0" || parts[0].Entry.Tokens != nil {
		t.Fatalf("direct request-based zero lost: %+v %v", parts, err)
	}
}

func TestCursorOverlappingOwnedConversationsStaySeparate(t *testing.T) {
	cfg, session := cursorBillingFixture()
	other := strings.ReplaceAll(strings.Replace(cursorLedgerEvent, "21.36232", "50", 1), `"owned"`, `"other-owned"`)
	body := strings.Replace(ledgerResponse(cursorLedgerEvent+","+other), `"totalUsageEventsCount":1`, `"totalUsageEventsCount":2`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	var group sync.WaitGroup
	for id, want := range map[string]contract.Amount{"owned": "0.2136232", "other-owned": "0.5"} {
		group.Add(1)
		go func(id string, want contract.Amount) {
			defer group.Done()
			owned := session
			owned.ID = id
			parts, err := fetchCursorWindow(context.Background(), server.Client(), server.URL, "key", cfg, owned)
			if err != nil || len(parts) != 1 || parts[0].Entry.Costs[0].Amount.Collected.Value != want {
				t.Errorf("cross-attributed concurrent sessions: %s %+v %v", id, parts, err)
			}
		}(id, want)
	}
	group.Wait()
}

func TestCursorCompletePaginationAndChangingCounts(t *testing.T) {
	for _, changing := range []bool{false, true} {
		cfg, session := cursorBillingFixture()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var query struct {
				Page int `json:"page"`
			}
			_ = json.NewDecoder(r.Body).Decode(&query)
			events := []json.RawMessage{}
			count := 1001
			if query.Page == 1 {
				events = append(events, json.RawMessage(cursorLedgerEvent))
				other := json.RawMessage(strings.ReplaceAll(cursorLedgerEvent, `"owned"`, `"unrelated"`))
				for i := 1; i < 1000; i++ {
					events = append(events, other)
				}
			} else {
				events = append(events, json.RawMessage(strings.ReplaceAll(cursorLedgerEvent, `"owned"`, `"unrelated"`)))
				if changing {
					count = 1002
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"totalUsageEventsCount": count, "pagination": map[string]any{"currentPage": query.Page, "numPages": 2, "pageSize": 1000, "hasNextPage": query.Page == 1}, "usageEvents": events})
		}))
		parts, err := fetchCursorWindow(context.Background(), server.Client(), server.URL, "key", cfg, session)
		server.Close()
		if changing {
			if err == nil || len(parts) != 0 {
				t.Fatal("mutating pagination accepted")
			}
		} else if err != nil || len(parts) != 1 {
			t.Fatalf("complete pages rejected: %+v %v", parts, err)
		}
	}
}
