package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

// reconcileCursorSnapshot returns a full replacement revision for the same
// attempt's evidence. It never submits another agent request, creates a second
// billable attempt, or mutates provider ledger records.
func reconcileCursorSnapshot(ctx context.Context, client *http.Client, endpoint, key string, cfg cursorBillingConfig, previous contract.Accounting) (*contract.Accounting, error) {
	raw, err := contract.CanonicalAccounting(previous)
	if err != nil {
		return nil, err
	}
	var next contract.Accounting
	if err := json.Unmarshal(raw, &next); err != nil {
		return nil, err
	}
	if !previous.Final || previous.Revision >= 9007199254740991 {
		return nil, fmt.Errorf("closed bounded accounting revision required")
	}
	var owned *contract.AccountingSession
	for i := range next.Sessions {
		s := &next.Sessions[i]
		if s.Provider == "cursor" && s.Source == "cursor-init" {
			if owned != nil {
				return nil, fmt.Errorf("ambiguous cursor conversation")
			}
			owned = s
		}
	}
	if owned == nil {
		return nil, fmt.Errorf("owned cursor conversation unavailable")
	}
	parts, lookupErr := fetchCursorWindow(ctx, client, endpoint, key, cfg, *owned)
	if lookupErr == nil {
		r := accounting.NewRecorder()
		for _, part := range parts {
			if err := r.Record(part); err != nil {
				return nil, err
			}
		}
		ledger, err := r.Finish()
		if err != nil {
			return nil, err
		}
		entries := next.Entries[:0]
		for _, entry := range next.Entries {
			oldLedger := len(entry.Costs) > 0 && entry.Tokens == nil && len(entry.Credits) == 0
			for _, cost := range entry.Costs {
				if cost.Amount.Collected == nil || cost.Amount.Collected.Source != "cursor-admin-usage" || cost.Amount.Estimated != nil {
					oldLedger = false
				}
			}
			if !oldLedger {
				entries = append(entries, entry)
			}
		}
		next.Entries = append(entries, ledger.Entries...)
	}
	for _, basis := range []string{"provider-metered", "model-reference"} {
		coverage := contract.MeterCoverage{Key: contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: basis}, State: "partial", Reason: "provider-window-provisional"}
		if lookupErr != nil {
			coverage.Reason = lookupErr.Error()
		}
		found := false
		for i := range next.Coverage {
			if next.Coverage[i].Key == coverage.Key {
				next.Coverage[i] = coverage
				found = true
			}
		}
		if !found {
			next.Coverage = append(next.Coverage, coverage)
		}
	}
	next.Revision++
	if _, err := contract.CanonicalAccounting(next); err != nil {
		return nil, err
	}
	return &next, nil
}

func runCursorReconciliation() error {
	var request struct {
		Accounting contract.Accounting `json:"accounting"`
		Billing    cursorBillingConfig `json:"billing"`
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, (128<<10)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("invalid reconciliation request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one reconciliation request")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	snapshot, err := reconcileCursorSnapshot(ctx, client, cursorUsageEndpoint, os.Getenv("CURSOR_ADMIN_API_KEY"), request.Billing, request.Accounting)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(snapshot)
}
