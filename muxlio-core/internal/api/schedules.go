package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/schedule"
	"github.com/muxlio/muxlio-core/internal/state"
)

func registerSchedules(mux *http.ServeMux, store state.Store) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		s, ok := store.(schedule.Store)
		if !ok {
			writeError(w, 503, CodeStoreError, "durable schedule storage unavailable")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/v1/schedules")
		if path == "" {
			switch r.Method {
			case http.MethodGet:
				items, err := s.ListSchedules()
				if err != nil {
					writeError(w, 500, CodeStoreError, err.Error())
					return
				}
				writeJSON(w, 200, map[string]any{"schedules": items})
			case http.MethodPost:
				var d schedule.Definition
				dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&d); err != nil {
					writeError(w, 400, CodeInvalidJSON, err.Error())
					return
				}
				var extra any
				if err := dec.Decode(&extra); err != io.EOF {
					writeError(w, 400, CodeInvalidJSON, "expected exactly one JSON value")
					return
				}
				raw, err := store.GetWorkflowVersion(d.WorkflowDigest)
				if err != nil {
					writeError(w, 400, CodeBadRequest, "registered workflow version required")
					return
				}
				var p plan.Plan
				if err := json.Unmarshal(raw, &p); err != nil {
					writeError(w, 400, CodeBadRequest, err.Error())
					return
				}
				if err := plan.ValidateSchedule(d, &p); err != nil {
					writeError(w, 400, CodeBadRequest, err.Error())
					return
				}
				if err := s.CreateSchedule(d); err != nil {
					writeError(w, 400, CodeBadRequest, err.Error())
					return
				}
				writeJSON(w, http.StatusCreated, d)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeError(w, 405, CodeMethodNotAllowed, "use GET or POST")
			}
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "fires" {
			writeError(w, 404, CodeNotFound, "unknown schedule endpoint")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, CodeMethodNotAllowed, "use GET")
			return
		}
		items, err := s.ListSchedules()
		if err != nil {
			writeError(w, 500, CodeStoreError, err.Error())
			return
		}
		found := false
		for _, item := range items {
			if item.ID == parts[0] {
				found = true
				break
			}
		}
		if !found {
			writeError(w, 404, CodeNotFound, "schedule not found")
			return
		}
		fires, err := s.ListScheduleFires(parts[0])
		if err != nil {
			writeError(w, 500, CodeStoreError, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"fires": fires})
	}
	mux.HandleFunc("/v1/schedules", handler)
	mux.HandleFunc("/v1/schedules/", handler)
}
