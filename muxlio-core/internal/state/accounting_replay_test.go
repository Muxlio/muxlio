package state_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
)

func TestAccountingProgressEventsAreIdempotentAndContainOnlyReferences(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "events.db"))
				if err != nil {
					t.Fatal(err)
				}
				s = db
			}
			defer s.Close()
			if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "activation"}); err != nil {
				t.Fatal(err)
			}
			a := testaccounting.Tokens(10, 2, 0)
			for i := 0; i < 3; i++ {
				if err := s.RecordAccountingSnapshot("attempt", *a); err != nil {
					t.Fatal(err)
				}
			}
			events, err := s.GetEvents("run", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			updates := 0
			for _, event := range events {
				if event.Type != state.EventAccountingUpdated {
					continue
				}
				updates++
				raw, err := json.Marshal(event.Payload)
				if err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload) != 3 || payload["attemptId"] != "attempt" || payload["revision"] != float64(1) || len(payload["hash"].(string)) != 64 {
					t.Fatalf("unexpected accounting event: %s", raw)
				}
			}
			if updates != 1 {
				t.Fatalf("replay emitted %d updates", updates)
			}
		})
	}
}

func TestTerminalReplayPreservesConfirmedAccounting(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, kind := range []string{"tokens-cost", "estimate", "credits"} {
			for _, terminal := range []string{"failed", "succeeded"} {
				t.Run(backend+"/"+kind+"/"+terminal, func(t *testing.T) {
					var s state.Store
					path := filepath.Join(t.TempDir(), "state.db")
					if backend == "memory" {
						s = state.New()
					} else {
						db, err := sqlite.Open(path)
						if err != nil {
							t.Fatal(err)
						}
						s = db
					}
					defer func() { s.Close() }()
					if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
						t.Fatal(err)
					}
					var snapshot *contract.Accounting
					switch kind {
					case "tokens-cost":
						snapshot = testaccounting.WithCosts(testaccounting.Tokens(10, 5, 0), "0.5", "")
					case "estimate":
						snapshot = testaccounting.WithCosts(nil, "", "0.0812")
					case "credits":
						snapshot = &contract.Accounting{Revision: 1, Entries: []contract.AccountingEntry{{ID: "call", Credits: []contract.CreditMeasure{{Provider: "service", Unit: "credit", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: "0.18065595854063016", Source: "fixture", Fidelity: "metered"}}}}}}}
					}
					if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", State: "running", Accounting: snapshot}); err != nil {
						t.Fatal(err)
					}
					for i := 0; i < 2; i++ {
						if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", State: terminal}); err != nil {
							t.Fatal(err)
						}
					}
					if backend == "sqlite" {
						if err := s.Close(); err != nil {
							t.Fatal(err)
						}
						db, err := sqlite.Open(path)
						if err != nil {
							t.Fatal(err)
						}
						s = db
						if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", State: terminal}); err != nil {
							t.Fatal(err)
						}
					}
					rows, err := s.ListExecutionAttempts("run", "")
					if err != nil || len(rows) != 1 || rows[0].State != terminal || !testaccounting.Equal(rows[0].Accounting, snapshot) {
						t.Fatalf("confirmed accounting lost: %+v %v", rows, err)
					}
				})
			}
		}
	}
}
