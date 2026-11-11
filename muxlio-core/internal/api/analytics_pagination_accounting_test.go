package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
)

type countedAnalyticsStore struct {
	state.Store
	queries int
	query   func(state.AnalyticsQuery) (*state.AnalyticsSnapshot, error)
}

func (s *countedAnalyticsStore) QueryAnalytics(q state.AnalyticsQuery) (*state.AnalyticsSnapshot, error) {
	s.queries++
	return s.query(q)
}

func TestAnalyticsAccountingUsesOnlyTheRequestedPage(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store
			if backend == "memory" {
				memory := state.New()
				t.Cleanup(func() { memory.Close() })
				store = memory
			} else {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				store = db
			}
			for i := 1; i <= 3; i++ {
				id := fmt.Sprint(i)
				at := time.Date(2026, 11, i, 12, 0, 0, 0, time.UTC)
				if err := store.CreateRun(&state.Run{ID: id, WorkflowName: "workflow-" + id, CreatedAt: at, UpdatedAt: at}); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt-" + id, RunID: id, ActivationID: "activation-" + id, Executor: "executor-" + id, State: "succeeded", StartedAt: at, FinishedAt: at, Accounting: testaccounting.Tokens(int64(i*100), 0, 0)}); err != nil {
					t.Fatal(err)
				}
			}
			counted := &countedAnalyticsStore{Store: store, query: store.(state.AnalyticsStore).QueryAnalytics}
			router := NewRouter(counted, nil)
			for offset := 0; offset <= 3; offset++ {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/v1/analytics?limit=1&offset=%d", offset), nil))
				if w.Code != 200 {
					t.Fatalf("%d: %s", w.Code, w.Body.String())
				}
				if counted.queries != offset+1 {
					t.Fatal("request scanned additional analytics pages")
				}
				var response struct {
					Runs                                                                                              []*state.Run
					AttemptsByRun                                                                                     map[string][]state.ExecutionAttempt
					Accounting                                                                                        state.AccountingSummary
					AccountingByRun, AccountingByAttempt, AccountingByDay, AccountingByExecutor, AccountingByWorkflow map[string]state.AccountingSummary
					AccountingPopulation                                                                              string
					TotalRuns                                                                                         int
					HasMore                                                                                           bool
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.TotalRuns != 3 || response.HasMore != (offset < 2) || !strings.Contains(response.AccountingPopulation, "returned page") {
					t.Fatalf("incorrect scope metadata: %+v", response)
				}
				if offset == 3 {
					if len(response.Runs) != 0 || len(response.AccountingByAttempt) != 0 || response.Accounting.Tokens.KnownSubtotal != nil {
						t.Fatal("empty page retained another page's consumption")
					}
					continue
				}
				id := fmt.Sprint(3 - offset)
				if len(response.Runs) != 1 || response.Runs[0].ID != id || len(response.AttemptsByRun) != 1 {
					t.Fatalf("incorrect run page: %+v", response.Runs)
				}
				want := fmt.Sprint((3 - offset) * 100)
				if response.Accounting.Tokens.EffectiveTotal == nil || *response.Accounting.Tokens.EffectiveTotal != want {
					t.Fatalf("page total: %+v", response.Accounting.Tokens)
				}
				for _, groups := range []map[string]state.AccountingSummary{response.AccountingByRun, response.AccountingByAttempt, response.AccountingByDay, response.AccountingByExecutor, response.AccountingByWorkflow} {
					if len(groups) != 1 {
						t.Fatalf("summary map escaped page bounds: %d", len(groups))
					}
					for _, summary := range groups {
						if summary.Tokens.KnownSubtotal == nil || *summary.Tokens.KnownSubtotal != want {
							t.Fatalf("group total differs from page: %+v", summary.Tokens)
						}
					}
				}
			}
		})
	}
}
