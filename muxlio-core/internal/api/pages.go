package api

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
	"net/http"
	"strconv"
	"time"
)

// nonNegativeEventSeq parses a non-empty event sequence cursor. Invalid or
// negative values write MUXLIO_BAD_EVENT_CURSOR and return ok=false.
func nonNegativeEventSeq(w http.ResponseWriter, value string) (seq int, ok bool) {
	if value == "" {
		return 0, true
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		writeError(w, http.StatusBadRequest, CodeBadEventCursor, "event cursor must be a non-negative sequence number")
		return 0, false
	}
	return n, true
}

// Pagination is opt-in for older clients. UI requests always supply a bounded limit.
func pageOptions(r *http.Request) (offset, limit int) {
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return
}
func pageResult[T any](items []T, offset, limit int, key string) map[string]any {
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	if items == nil {
		items = []T{}
	}
	return map[string]any{key: items, "offset": offset, "nextOffset": offset + len(items), "hasMore": more, "limit": limit}
}

func handleRunOutputs(w http.ResponseWriter, r *http.Request, s state.Store, id string) {
	if r.Method != http.MethodGet {
		writeError(w, 405, CodeMethodNotAllowed, "use GET")
		return
	}
	if id != "" {
		if _, err := runHeader(s, id); err != nil {
			writeError(w, 404, CodeNotFound, "run not found")
			return
		}
	}

	views, ok := s.(state.RunViewStore)
	if !ok {
		writeError(w, 503, CodeStoreError, "paged reads unavailable")
		return
	}
	offset, limit := pageOptions(r)
	items, err := views.ListOutputPage(id, offset, limit+1, r.URL.Query().Get("q"))
	if err != nil {
		writeError(w, 500, CodeStoreError, err.Error())
		return
	}
	writeJSON(w, 200, pageResult(items, offset, limit, "outputs"))
}

// Summaries read accounting columns only, never attempt results or log payloads.
func handleRunSummary(w http.ResponseWriter, r *http.Request, s state.Store, run *state.Run) {
	views, ok := s.(state.RunViewStore)
	if !ok {
		writeError(w, 503, CodeStoreError, "paged reads unavailable")
		return
	}
	attempts, err := views.ListExecutionAttemptPage(run.ID, "", 0, -1, false)
	if err != nil {
		writeError(w, 500, CodeStoreError, err.Error())
		return
	}
	total, completed, err := views.ActivationCounts(run.ID)
	if err != nil {
		writeError(w, 500, CodeStoreError, err.Error())
		return
	}
	states := map[string]int{}
	held := 0
	var elapsed *int64
	missingTiming := false
	for _, a := range attempts {
		states[a.State]++
		if a.AwaitReconciliationReason != "" {
			held++
		}
		ms := a.ExecutionDurationMS
		if ms == nil && a.State != "running" && !a.StartedAt.IsZero() && !a.FinishedAt.IsZero() && !a.FinishedAt.Before(a.StartedAt) {
			n := a.FinishedAt.Sub(a.StartedAt) / time.Millisecond
			v := int64(n)
			ms = &v
		}
		if ms != nil {
			if elapsed == nil {
				elapsed = new(int64)
			}
			*elapsed += *ms
		} else if a.State != "running" && a.State != "queued" {
			missingTiming = true
		}
	}
	if missingTiming {
		elapsed = nil
	}
	latest, err := views.LatestEventSeq(run.ID)
	if err != nil {
		writeError(w, 500, CodeStoreError, err.Error())
		return
	}
	safe := *run
	var pinned struct {
		Limits          any      `json:"limits"`
		DeadlineSeconds *float64 `json:"deadlineSeconds"`
	}
	_ = json.Unmarshal(run.PlanJSON, &pinned)
	safe.Input = nil
	safe.Output = nil
	writeJSON(w, 200, map[string]any{"id": safe.ID, "workflowName": safe.WorkflowName, "state": safe.State, "createdAt": safe.CreatedAt, "updatedAt": safe.UpdatedAt,
		"workspaceId": run.WorkspaceID, "originLabel": run.OriginLabel, "callerId": run.CallerID,
		"planDigest": run.PlanDigest, "fork": run.Fork, "pinned": run.Pinned, "retainedUntil": run.RetainedUntil, "error": diagnostic.Redact(run.Error),
		"limits": pinned.Limits, "deadlineSeconds": pinned.DeadlineSeconds,
		"overview": map[string]any{"attemptStates": states, "heldAttempts": held, "activations": total, "completed": completed, "attempts": len(attempts), "latestEventSeq": latest, "accounting": accountingSummary(attempts), "executionDurationMs": elapsed}})
}

func accountingSummary(attempts []state.ExecutionAttempt) any {
	summary, err := state.SummarizeAccounting(attempts)
	if err != nil {
		return nil
	}
	return summary
}

func runHeader(s state.Store, id string) (*state.Run, error) {
	if views, ok := s.(state.RunViewStore); ok {
		return views.GetRunHeader(id)
	}
	return s.GetRun(id)
}
