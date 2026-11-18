package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func registerAvailability(mux *http.ServeMux, store state.Store) {
	availability, _ := store.(workers.AvailabilityStore)
	mux.HandleFunc("/v1/executors/availability", func(w http.ResponseWriter, r *http.Request) {
		if availability == nil {
			writeError(w, http.StatusServiceUnavailable, CodeStoreError, "availability store unavailable")
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		rows, err := availability.ListExecutorUnavailability()
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
			return
		}
		if rows == nil {
			rows = []state.ExecutorUnavailability{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"unavailable": rows})
	})
	mux.HandleFunc("/v1/executors/availability/recheck", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		if availability == nil {
			writeError(w, http.StatusServiceUnavailable, CodeStoreError, "availability store unavailable")
			return
		}
		var body struct {
			WorkerID string `json:"workerId"`
			Executor string `json:"executor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid recheck request")
			return
		}
		body.WorkerID = strings.TrimSpace(body.WorkerID)
		body.Executor = strings.TrimSpace(body.Executor)
		if body.WorkerID == "" || body.Executor == "" {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "workerId and executor are required")
			return
		}
		now := time.Now()
		current, err := availability.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: body.WorkerID, Executor: body.Executor})
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
			return
		}
		if current == nil {
			writeJSON(w, http.StatusOK, map[string]any{"cleared": true, "workerId": body.WorkerID, "executor": body.Executor})
			return
		}
		// Explicit API recheck may clear authentication failures after operator
		// attestation. Quota blocks are never cleared by a no-op probe; they
		// require a successful assignment after RetryAt.
		if current.Reason == contract.AvailabilityQuotaExhausted {
			msg := "quota exhaustion cannot be cleared by login alone; wait for retryAt and a successful worker assignment"
			if current.RetryAt != nil {
				msg = fmt.Sprintf("%s (retryAt %s)", msg, current.RetryAt.UTC().Format(time.RFC3339))
			}
			writeError(w, http.StatusConflict, CodeRunControlRejected, msg)
			return
		}
		err = workers.RecheckExecutor(availability, body.WorkerID, body.Executor, now, true, func() (*contract.Availability, error) {
			return nil, nil
		})
		if err != nil {
			writeError(w, http.StatusConflict, CodeRunControlRejected, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cleared": true, "workerId": body.WorkerID, "executor": body.Executor})
	})
}
