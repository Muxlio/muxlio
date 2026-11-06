package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

// GET input-requests lists every request, preserving activation identity even
// when concurrent waits share a name. POST input-requests/:requestID delivers
// one JSON response; it never falls back to the name-based signal queue.
func handleInputRequests(w http.ResponseWriter, r *http.Request, store state.Store, runID string, rest []string) {
	if _, err := store.GetRun(runID); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	if _, err := store.ExpireInputRequests(time.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	if len(rest) == 0 && r.Method == http.MethodGet {
		requests, err := store.ListInputRequests(runID)
		if err != nil {
			writeError(w, 500, CodeStoreError, err.Error())
			return
		}
		if requests == nil {
			requests = []state.InputRequest{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
		return
	}
	if len(rest) != 1 || rest[0] == "" || strings.Contains(rest[0], "/") || r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET input-requests or POST input-requests/:requestID")
		return
	}
	req, err := store.GetInputRequest(runID, rest[0])
	if err != nil {
		writeError(w, 404, CodeNotFound, err.Error())
		return
	}
	caller := CallerFromRequest(r)
	if caller.ID == "" || !caller.HasAllPermissions(req.Permissions) {
		writeError(w, http.StatusForbidden, CodeUnauthorized, "caller lacks required input request permissions")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, 400, CodeInvalidJSON, "Idempotency-Key is required")
		return
	}
	var response any
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.UseNumber()
	if err := dec.Decode(&response); err != nil {
		writeError(w, 400, CodeInvalidJSON, "expected a JSON response")
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, 400, CodeInvalidJSON, "expected exactly one JSON value")
		return
	}
	if err := plan.ValidateInputResponse(req.Schema, response); err != nil {
		writeError(w, http.StatusUnprocessableEntity, plan.InputResponseValidationFailed, err.Error())
		return
	}
	result, accepted, err := store.SubmitInputResponse(runID, req.RequestID, response, caller.ID, key)
	if errors.Is(err, state.ErrInputRequestNotAccepting) {
		writeError(w, 409, CodeInputRequestClosed, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, CodeStoreError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": result, "accepted": accepted})
}
