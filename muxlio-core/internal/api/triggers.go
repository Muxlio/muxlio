package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/trigger"
)

// persistedRunStarter attaches execution to a run already committed by a trigger.
// Creation and idempotency must share a transaction, before execution starts.
type persistedRunStarter interface{ StartPersistedRun(string) error }

func registerTriggers(mux *http.ServeMux, store state.Store, runner Runner) {
	management := func(w http.ResponseWriter, r *http.Request) {
		s, ok := store.(trigger.Store)
		if !ok {
			writeError(w, 503, CodeStoreError, "durable trigger storage unavailable")
			return
		}
		if r.URL.Path == "/v1/triggers" {
			switch r.Method {
			case http.MethodGet:
				items, err := s.ListTriggers()
				if err != nil {
					writeError(w, 500, CodeStoreError, err.Error())
					return
				}
				if items == nil {
					items = []trigger.Trigger{}
				}
				writeJSON(w, 200, map[string]any{"triggers": items})
			case http.MethodPost:
				// Secret is write-only; Trigger's response representation never exposes it.
				var req struct {
					trigger.Trigger
					Secret  string `json:"secret"`
					Enabled *bool  `json:"enabled"`
				}
				dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&req); err != nil {
					writeError(w, 400, CodeInvalidJSON, err.Error())
					return
				}
				var extra any
				if err := dec.Decode(&extra); err != io.EOF {
					writeError(w, 400, CodeInvalidJSON, "expected exactly one JSON value")
					return
				}
				tr := req.Trigger
				tr.Secret = req.Secret
				tr.Enabled = req.Enabled == nil || *req.Enabled
				if err := tr.Normalize(); err != nil {
					writeError(w, 400, CodeBadRequest, err.Error())
					return
				}
				raw, err := store.GetWorkflowVersion(tr.WorkflowDigest)
				var p plan.Plan
				if err != nil || json.Unmarshal(raw, &p) != nil || p.VerifyDigest() != nil || p.Digest != tr.WorkflowDigest {
					writeError(w, 400, CodeBadRequest, "registered immutable workflow version required")
					return
				}
				if err := s.CreateTrigger(tr); err != nil {
					writeError(w, 400, CodeBadRequest, err.Error())
					return
				}
				writeJSON(w, 201, tr)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeError(w, 405, CodeMethodNotAllowed, "use GET or POST")
			}
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/triggers/"), "/")
		if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] != "deliveries") {
			writeError(w, 404, CodeNotFound, "unknown trigger endpoint")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, 405, CodeMethodNotAllowed, "use GET")
			return
		}
		tr, err := s.GetTrigger(parts[0])
		if err != nil {
			writeError(w, 404, CodeNotFound, "trigger not found")
			return
		}
		if len(parts) == 1 {
			writeJSON(w, 200, tr)
			return
		}
		items, err := s.ListDeliveries(tr.ID)
		if err != nil {
			writeError(w, 500, CodeStoreError, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"deliveries": items})
	}
	mux.HandleFunc("/v1/triggers", management)
	mux.HandleFunc("/v1/triggers/", management)
	// This data-plane route uses trigger-specific HMAC authentication even on
	// loopback. It never accepts the control-plane bearer token as a substitute.
	mux.HandleFunc("/webhooks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, 405, CodeMethodNotAllowed, "use POST")
			return
		}
		s, ok := store.(trigger.Store)
		if !ok {
			writeError(w, 503, CodeStoreError, "durable trigger storage unavailable")
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/webhooks/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, 404, CodeNotFound, "trigger not found")
			return
		}
		tr, err := s.GetTrigger(id)
		if err != nil {
			writeError(w, 404, CodeNotFound, "trigger not found")
			return
		}
		key := r.Header.Get("Idempotency-Key")
		reject := func(status int, code, reason string) {
			if err := s.RecordDelivery(trigger.Delivery{TriggerID: id, WorkflowDigest: tr.WorkflowDigest, State: trigger.DeliveryRejected, Reason: reason}); err != nil {
				writeError(w, 500, CodeStoreError, "could not record rejected delivery")
				return
			}
			writeError(w, status, code, reason)
		}
		if !tr.Enabled {
			reject(403, CodeUnauthorized, "trigger disabled")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(tr.PayloadLimitBytes)))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				reject(413, CodeBadRequest, "webhook payload exceeds limit")
			} else {
				reject(400, CodeBadRequest, "could not read webhook payload")
			}
			return
		}
		if err := tr.VerifySignature(body, r.Header.Get(tr.SignatureHeader)); err != nil {
			reject(401, CodeUnauthorized, "invalid or missing webhook signature")
			return
		}
		if len(key) > 256 {
			reject(400, CodeBadRequest, "idempotency key exceeds 256 bytes")
			return
		}
		input, err := tr.MapInput(body)
		if err != nil {
			reject(400, CodeBadRequest, err.Error())
			return
		}
		raw, err := store.GetWorkflowVersion(tr.WorkflowDigest)
		var p plan.Plan
		if err != nil || json.Unmarshal(raw, &p) != nil || p.VerifyDigest() != nil || p.Digest != tr.WorkflowDigest {
			reject(500, CodeStoreError, "pinned workflow unavailable")
			return
		}
		if err := plan.ValidateInput(&p, input); err != nil {
			reject(400, CodeBadRequest, err.Error())
			return
		}
		result, err := s.DeliverWebhook(trigger.WebhookDeliveryRequest{TriggerID: id, IdempotencyKey: key, WorkflowDigest: tr.WorkflowDigest, WorkflowName: p.Name, PlanJSON: raw, Input: input, DeduplicationSeconds: tr.DeduplicationSeconds})
		if err != nil {
			writeError(w, 500, CodeStoreError, err.Error())
			return
		}
		if starter, ok := runner.(persistedRunStarter); ok {
			// Retry attachment on duplicate delivery too: a prior response may have
			// failed after commit. The starter is idempotent and recovery handles crashes.
			if err := starter.StartPersistedRun(result.RunID); err != nil {
				writeError(w, 503, CodeStoreError, "run persisted; execution startup failed, retry delivery")
				return
			}
		}
		status := http.StatusCreated
		if result.Duplicate {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{"runId": result.RunID, "workflowDigest": result.WorkflowDigest, "duplicate": result.Duplicate})
	})
}
