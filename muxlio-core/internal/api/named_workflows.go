package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

const maxWorkflowPlanBytes = 8 << 20

var workflowIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func registerNamedWorkflows(mux *http.ServeMux, store state.Store) {
	mux.HandleFunc("/v1/workflows", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
			return
		}
		var request struct {
			WorkflowID string          `json:"workflowId"`
			Plan       json.RawMessage `json:"plan"`
		}
		if !decodeWorkflowPublication(w, r, &request) || !checkWorkflowID(w, request.WorkflowID) {
			return
		}
		publishNamedWorkflow(w, store, request.WorkflowID, 0, request.Plan, "", http.StatusCreated)
	})
	mux.HandleFunc("/v1/workflows/{workflowId}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("workflowId")
		if !checkWorkflowID(w, id) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			workflow, err := store.GetNamedWorkflow(id)
			writeNamedWorkflow(w, workflow, err, http.StatusOK)
		case http.MethodPost:
			var request struct {
				ExpectedCurrentRevision int64           `json:"expectedCurrentRevision"`
				Plan                    json.RawMessage `json:"plan"`
				PlanDigest              string          `json:"planDigest"`
			}
			if !decodeWorkflowPublication(w, r, &request) {
				return
			}
			if request.ExpectedCurrentRevision <= 0 {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "expectedCurrentRevision must be a positive integer")
				return
			}
			publishNamedWorkflow(w, store, id, request.ExpectedCurrentRevision, request.Plan, request.PlanDigest, http.StatusOK)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET or POST")
		}
	})
}

func checkWorkflowID(w http.ResponseWriter, id string) bool {
	if !workflowIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "workflowId must start with a letter or digit and contain at most 128 letters, digits, dots, hyphens, or underscores")
		return false
	}
	return true
}

func decodeWorkflowPublication(w http.ResponseWriter, r *http.Request, request any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflowPlanBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(request); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
		return false
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, "expected exactly one JSON value")
		return false
	}
	return true
}

func publishNamedWorkflow(w http.ResponseWriter, store state.Store, id string, expected int64, raw json.RawMessage, digest string, status int) {
	hasPlan := len(raw) > 0 && string(raw) != "null"
	if hasPlan == (digest != "") {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "select exactly one of plan or planDigest")
		return
	}
	publication := state.WorkflowPublication{Digest: digest}
	if hasPlan {
		p, data, ok := validateWorkflowPlan(w, raw)
		if !ok {
			return
		}
		publication = state.WorkflowPublication{Digest: p.Digest, Name: p.Name, PlanJSON: data}
	}
	workflow, err := store.PublishWorkflow(id, expected, publication)
	writeNamedWorkflow(w, workflow, err, status)
}

func writeNamedWorkflow(w http.ResponseWriter, workflow state.NamedWorkflow, err error, status int) {
	switch {
	case err == nil:
		writeJSON(w, status, workflow)
	case errors.Is(err, state.ErrNamedWorkflowNotFound), errors.Is(err, state.ErrWorkflowVersionNotPublished):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, state.ErrNamedWorkflowConflict):
		writeError(w, http.StatusConflict, CodeNamedWorkflowConflict, err.Error())
	case errors.Is(err, state.ErrWorkflowVersionRetired):
		writeError(w, http.StatusGone, CodeWorkflowVersionRetired, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
	}
}

func validateWorkflowPlan(w http.ResponseWriter, data []byte) (*plan.Plan, []byte, bool) {
	var p plan.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, "invalid plan JSON: "+err.Error())
		return nil, nil, false
	}
	if errs := plan.Validate(&p); plan.HasErrors(errs) {
		writeValidationErrors(w, plan.ErrorsOnly(errs))
		return nil, nil, false
	}
	if err := p.VerifyDigest(); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
		return nil, nil, false
	}
	if p.Digest == "" {
		digest, err := p.ComputeDigest()
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, "computing digest: "+err.Error())
			return nil, nil, false
		}
		p.Digest = digest
	}
	// Persist the verified plan with its digest in a consistent encoding so
	// whitespace and an omitted digest do not conflict with an existing pin.
	data, err := json.Marshal(&p)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
		return nil, nil, false
	}
	return &p, data, true
}
