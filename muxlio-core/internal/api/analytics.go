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
				attempts[id] = []state.ExecutionAttempt{}
				continue
			}
			attempts[id] = list
		}
		writeJSON(w, http.StatusOK, map[string]any{
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
