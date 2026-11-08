package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// GET/POST /runs/:run/sessions/:session/inputs exposes durable receipts.
func handleSessionInputs(w http.ResponseWriter, r *http.Request, store state.Store, runID, rest string) {
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "inputs" {
		writeError(w, 404, CodeNotFound, "session input route not found")
		return
	}
	q, err := store.GetSessionQueue(runID, parts[0])
	if err != nil {
		writeError(w, 404, CodeNotFound, err.Error())
		return
	}
	caller := CallerFromRequest(r)
	if r.Method == http.MethodGet {
		writeJSON(w, 200, q)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, CodeMethodNotAllowed, "use GET or POST")
		return
	}
	if caller.ID == "" || !caller.HasPermission("steer") || !caller.HasAllPermissions(q.Permissions) {
		writeError(w, 403, CodeUnauthorized, "caller lacks session input permissions")
		return
	}
	var body struct {
		Version   string `json:"version"`
		RequestID string `json:"requestId"`
		Message   string `json:"message"`
		Policy    string `json:"policy"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, 400, CodeInvalidJSON, err.Error())
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		writeError(w, 400, CodeInvalidJSON, "expected exactly one JSON object")
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" && key != body.RequestID {
		writeError(w, 400, CodeInvalidJSON, "Idempotency-Key must equal requestId")
		return
	}
	run, err := store.GetRun(runID)
	if err != nil {
		writeError(w, 404, CodeNotFound, err.Error())
		return
	}
	duplicate := false
	for _, input := range q.Inputs {
		if input.RequestID == body.RequestID {
			duplicate = true
			break
		}
	}
	if !duplicate && (run.State == "cancelled" || run.State == "failed" || run.State == "succeeded") {
		writeError(w, 409, CodeInvalidJSON, "run is terminal")
		return
	}
	q, err = state.AcceptSessionInput(store, runID, parts[0], state.QueuedSessionInput{
		SessionInput: contract.SessionInput{Version: body.Version, RequestID: body.RequestID, Message: body.Message},
		CallerID:     caller.ID, Policy: body.Policy,
	})
	if err != nil {
		code := 422
		if errors.Is(err, state.ErrSessionInputConflict) {
			code = 409
		}
		writeError(w, code, CodeInvalidJSON, err.Error())
		return
	}
	// Accepted does not mean delivered. Retrying returns the same durable receipt.
	for _, input := range q.Inputs {
		if input.RequestID == body.RequestID {
			writeJSON(w, http.StatusAccepted, input)
			return
		}
	}
}

func (c *Coordinator) registerSessionInputs(req workers.ExecuteRequest, result workers.ExecuteResult) error {
	if result.Session == nil || result.Error != "" {
		return nil
	}
	s := c.store
	candidates, err := c.store.ListWorkers(time.Time{})
	if err != nil {
		return err
	}
	for _, worker := range candidates {
		if worker.ID != result.Session.OwnerWorker {
			continue
		}
		for _, cap := range worker.Capabilities {
			if cap.Name != req.Executor {
				continue
			}
			m := cap.Contract
			boundary := m.Supports(contract.CapabilitySessionInput) && m.Check(req.Executor, "", "", map[string]string{contract.CapabilitySessionInput: contract.SessionInputVersion}) == nil
			if !boundary {
				return nil
			}
			q := &state.SessionQueue{Version: contract.SessionInputVersion, RunID: req.RunID, Handle: *result.Session,
				Boundary: boundary, Live: m.Supports(contract.CapabilityLiveInput) && m.Check(req.Executor, "", "", map[string]string{contract.CapabilityLiveInput: contract.SessionInputVersion}) == nil, Permissions: []string{"steer"}, Inputs: []state.QueuedSessionInput{}}
			// Existing queue identity and permissions are immutable.
			created, err := s.CompareAndSwapSessionQueue(q, 0)
			if err != nil || created {
				return err
			}
			existing, err := s.GetSessionQueue(req.RunID, q.Handle.ID)
			if err != nil {
				return err
			}
			if !existing.Handle.Equal(q.Handle) {
				return errors.New("session queue identity conflict")
			}
			return nil
		}
	}
	return nil
}

type liveInputReceipt struct {
	AssignmentID       string   `json:"assignmentId"`
	Generation         int64    `json:"generation"`
	CoordinatorEpoch   int64    `json:"coordinatorEpoch"`
	AcknowledgedInputs []string `json:"acknowledgedInputs"`
}

func (c *Coordinator) applySessionInputResult(req workers.ExecuteRequest, result workers.ExecuteResult) error {
	if err := c.registerSessionInputs(req, result); err != nil {
		return err
	}
	if req.Session != nil && req.Session.Mode == "resume" && req.Session.Handle != nil && len(result.AcknowledgedInputs) > 0 {
		return state.AcknowledgeSessionInputs(c.store, req.RunID, req.Session.Handle.ID, req.SessionInputs, result.AcknowledgedInputs)
	}
	return nil
}

func (c *Coordinator) assignmentRequest(id string) (*workers.ExecuteRequest, error) {
	raw, err := c.store.AssignmentRequest(id)
	if err != nil || raw == nil {
		return nil, err
	}
	var req workers.ExecuteRequest
	if err := contract.DecodeJSON(raw, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (c *Coordinator) liveInputCommand(a *state.Assignment) (any, error) {
	req, err := c.assignmentRequest(a.AssignmentID)
	if err != nil || req == nil || req.Session == nil || req.Session.Mode != "resume" || req.Session.Handle == nil {
		return nil, err
	}
	s := c.store
	q, err := s.GetSessionQueue(a.RunID, req.Session.Handle.ID)
	if errors.Is(err, state.ErrSessionQueueNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !q.Live || !q.Handle.Equal(*req.Session.Handle) {
		return nil, nil
	}
	pending := false
	for _, input := range q.Inputs {
		if input.Status == "accepted" && input.Delivery == "live" {
			pending = true
			break
		}
	}
	if !pending {
		return nil, nil
	}
	inputs := []contract.SessionInput{}
	// Persist the offer before sending it. A delayed receipt may arrive after
	// completion or boundary fallback, but must belong to this assignment.
	_, err = state.UpdateSessionQueue(s, a.RunID, q.Handle.ID, func(q *state.SessionQueue) error {
		inputs = nil
		for i := range q.Inputs {
			input := &q.Inputs[i]
			if input.Status == "accepted" && input.Delivery == "live" {
				input.LiveAttemptID = a.AttemptID
				inputs = append(inputs, input.SessionInput)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	return map[string]any{"sessionInput": map[string]any{"assignmentId": a.AssignmentID, "inputs": inputs}}, nil
}

func (c *Coordinator) acknowledgeLiveInputs(workerID string, receipts []liveInputReceipt) error {
	for _, receipt := range receipts {
		a, err := c.store.LookupWorkerAssignment(workerID, receipt.AssignmentID)
		if err != nil {
			return err
		}
		if a == nil || a.Generation != receipt.Generation || a.CoordinatorEpoch != receipt.CoordinatorEpoch {
			continue
		}
		req, err := c.assignmentRequest(a.AssignmentID)
		if err != nil {
			return err
		}
		if req == nil || req.Session == nil || req.Session.Mode != "resume" || req.Session.Handle == nil {
			continue
		}
		s := c.store
		q, err := s.GetSessionQueue(a.RunID, req.Session.Handle.ID)
		if err != nil {
			return err
		}
		if !q.Live || !q.Handle.Equal(*req.Session.Handle) {
			continue
		}
		offered := []contract.SessionInput{}
		for _, input := range q.Inputs {
			if input.LiveAttemptID == a.AttemptID {
				offered = append(offered, input.SessionInput)
			}
		}
		if err := state.AcknowledgeSessionInputs(s, a.RunID, q.Handle.ID, offered, receipt.AcknowledgedInputs); err != nil {
			return err
		}
	}
	return nil
}
