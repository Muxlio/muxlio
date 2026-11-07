package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

// Forks are created without execution unless start is explicitly requested.
func handleForkRun(w http.ResponseWriter, r *http.Request, s state.Store, runner Runner, sourceID string) {
	var request struct {
		Plan             *plan.Plan      `json:"plan,omitempty"`
		PlanDigest       string          `json:"planDigest,omitempty"`
		Input            json.RawMessage `json:"input"`
		ReuseActivations []string        `json:"reuseActivations"`
		Start            bool            `json:"start"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, 400, CodeInvalidJSON, err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, 400, CodeInvalidJSON, "request body must contain one JSON object")
		return
	}
	starter, canStart := runner.(interface{ StartPersistedRun(string) error })
	if request.Start && !canStart {
		writeError(w, 503, CodeRunnerUnavailable, "run execution is unavailable")
		return
	}
	if request.Plan != nil && request.PlanDigest != "" {
		writeError(w, 400, CodeInvalidJSON, "select plan or planDigest")
		return
	}
	if _, err := s.GetRun(sourceID); err != nil {
		writeError(w, 404, CodeNotFound, err.Error())
		return
	}
	if request.PlanDigest != "" {
		raw, err := s.GetWorkflowVersion(request.PlanDigest)
		if err != nil {
			writeError(w, 404, CodeNotFound, err.Error())
			return
		}
		request.Plan = &plan.Plan{}
		if err := json.Unmarshal(raw, request.Plan); err != nil {
			writeError(w, 500, CodeStoreError, err.Error())
			return
		}
	}
	engine := plan.NewEngineWithStore(request.Plan, nil, s)
	id, err := engine.ForkRun(sourceID, request.Input, request.ReuseActivations)
	if err != nil {
		writeError(w, 400, CodeRunError, err.Error())
		return
	}
	if request.Start {
		if err := starter.StartPersistedRun(id); err != nil {
			writeError(w, 500, CodeRunError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"runId": id, "started": request.Start})
}

func handleCompareRuns(w http.ResponseWriter, r *http.Request, s state.Store, id string) {
	other := r.URL.Query().Get("other")
	if other == "" {
		writeError(w, 400, CodeMissingRunID, "comparison requires other run ID")
		return
	}
	comparison, err := state.CompareRuns(s, id, other)
	if err != nil {
		writeError(w, 404, CodeNotFound, err.Error())
		return
	}
	comparison.Before.Error = diagnostic.Redact(comparison.Before.Error)
	comparison.After.Error = diagnostic.Redact(comparison.After.Error)
	for _, row := range comparison.Activations {
		if row.Before != nil {
			row.Before.Error = diagnostic.Redact(row.Before.Error)
		}
		if row.After != nil {
			row.After.Error = diagnostic.Redact(row.After.Error)
		}
	}
	writeJSON(w, 200, comparison)
}

func handleStartRun(w http.ResponseWriter, r *http.Request, s state.Store, runner Runner, id string) {
	run, err := s.GetRun(id)
	if err != nil {
		writeError(w, 404, CodeNotFound, err.Error())
		return
	}
	if run.State != plan.RunCreated {
		writeError(w, 409, CodeRunControlRejected, "only a created run can be started")
		return
	}
	starter, ok := runner.(interface{ StartPersistedRun(string) error })
	if !ok {
		writeError(w, 503, CodeRunnerUnavailable, "run execution is unavailable")
		return
	}
	if err := starter.StartPersistedRun(id); err != nil {
		writeError(w, 500, CodeRunError, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"runId": id})
}
