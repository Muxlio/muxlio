package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func registerAnalytics(mux *http.ServeMux, store state.Store) {
	mux.HandleFunc("/v1/analytics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
			return
		}
		analytics, ok := store.(state.AnalyticsStore)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, CodeBadRequest, "analytics query is unavailable")
			return
		}
		q, err := parseAnalyticsQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
			return
		}
		snapshot, err := analytics.QueryAnalytics(q)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
			return
		}
		attempts := map[string]any{}
		for id, list := range snapshot.AttemptsByRun {
			if list == nil {
				list = []state.ExecutionAttempt{}
			}
			attempts[id] = list
		}
		population := snapshot
		accountingByRun := map[string]any{}
		accountingByAttempt := map[string]any{}
		byDay := map[string][]state.ExecutionAttempt{}
		byExecutor := map[string][]state.ExecutionAttempt{}
		byWorkflow := map[string][]state.ExecutionAttempt{}
		workflows := map[string]string{}
		for _, run := range population.Runs {
			workflows[run.ID] = run.WorkflowName
		}
		var allAttempts []state.ExecutionAttempt
		for id, list := range population.AttemptsByRun {
			if list == nil {
				continue
			}
			summary, err := state.SummarizeAccounting(list)
			if err != nil {
				writeError(w, http.StatusInternalServerError, CodeStoreError, "accounting summary unavailable")
				return
			}
			accountingByRun[id] = summary
			allAttempts = append(allAttempts, list...)
			for _, a := range list {
				attemptSummary, err := state.SummarizeAccounting([]state.ExecutionAttempt{a})
				if err != nil {
					writeError(w, http.StatusInternalServerError, CodeStoreError, "accounting summary unavailable")
					return
				}
				accountingByAttempt[a.ID] = attemptSummary
				byExecutor[a.Executor] = append(byExecutor[a.Executor], a)
				byWorkflow[workflows[id]] = append(byWorkflow[workflows[id]], a)
				if !a.FinishedAt.IsZero() {
					day := a.FinishedAt.UTC().Format("2006-01-02")
					byDay[day] = append(byDay[day], a)
				}
			}
		}
		summary, err := state.SummarizeAccounting(allAttempts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, "accounting summary unavailable")
			return
		}
		groupSummaries := func(groups map[string][]state.ExecutionAttempt) (map[string]any, error) {
			out := map[string]any{}
			for key, attempts := range groups {
				s, err := state.SummarizeAccounting(attempts)
				if err != nil {
					return nil, err
				}
				out[key] = s
			}
			return out, nil
		}
		days, err := groupSummaries(byDay)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, "accounting summary unavailable")
			return
		}
		executors, err := groupSummaries(byExecutor)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, "accounting summary unavailable")
			return
		}
		workflowSummaries, err := groupSummaries(byWorkflow)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, "accounting summary unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"accounting": summary, "accountingByRun": accountingByRun, "accountingByAttempt": accountingByAttempt, "accountingByDay": days, "accountingByExecutor": executors, "accountingByWorkflow": workflowSummaries, "accountingPopulation": "returned page of filtered runs; daily amounts use attempt completion date in UTC",
			"runs":          snapshot.Runs,
			"attemptsByRun": attempts,
			"totalRuns":     snapshot.TotalRuns,
			"stateCounts":   snapshot.StateCounts,
			"offset":        snapshot.Offset,
			"limit":         snapshot.Limit,
			"hasMore":       snapshot.HasMore,
			"populations":   snapshot.Populations,
		})
	})
}

func parseAnalyticsQuery(r *http.Request) (state.AnalyticsQuery, error) {
	q := state.AnalyticsQuery{
		State:    strings.TrimSpace(r.URL.Query().Get("state")),
		Workflow: strings.TrimSpace(r.URL.Query().Get("workflow")),
		Executor: strings.TrimSpace(r.URL.Query().Get("executor")),
	}
	if q.Workflow == "" {
		q.Workflow = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, err
		}
		q.CreatedAfter = t.UTC()
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, err
		}
		q.CreatedBefore = t.UTC()
	}
	q.Offset, q.Limit = analyticsPageOptions(r)
	return q, nil
}

// analyticsPageOptions applies analytics-specific pagination bounds (default 200, max 500).
func analyticsPageOptions(r *http.Request) (offset, limit int) {
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return offset, 200
	}
	limit, _ = strconv.Atoi(raw)
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	return offset, limit
}
