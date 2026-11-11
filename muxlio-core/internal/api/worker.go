package api

import (
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

// WorkerProtocolVersion is the current outbound worker wire contract.
const WorkerProtocolVersion = "v1"

// WorkerProtocolBackend owns worker identity, leases, and generic execution
// envelopes. Implementations must fence completion by assignment generation.
type WorkerProtocolBackend interface {
	Register(WorkerRegistration) (WorkerRegistrationResult, error)
	Heartbeat(workerID string, req WorkerHeartbeat) (WorkerHeartbeatResult, error)
	NextAssignment(workerID string) (*WorkerAssignment, error)
	Complete(workerID string, req WorkerCompletion) error
	AppendWorkerLog(workerID string, req WorkerLogAppend) error
	RecordWorkerAccounting(workerID string, req WorkerAccountingAppend) error
}

// WorkerRegistration describes the runtime and capabilities of a worker.
type WorkerRegistration struct {
	SupportedProtocolVersions []string           `json:"supportedProtocolVersions,omitempty"`
	ProtocolVersion           string             `json:"protocolVersion"`
	WorkerID                  string             `json:"workerId"`
	InstanceID                string             `json:"instanceId,omitempty"`
	OS                        string             `json:"os,omitempty"`
	Architecture              string             `json:"architecture,omitempty"`
	Capacity                  int                `json:"capacity"`
	Capabilities              []WorkerCapability `json:"capabilities"`
	Labels                    map[string]string  `json:"labels,omitempty"`
}

type WorkerCapability = state.WorkerCapability

type WorkerRegistrationResult struct {
	ProtocolVersion  string `json:"protocolVersion,omitempty"`
	CoordinatorEpoch int64  `json:"coordinatorEpoch"`
	HeartbeatSeconds int    `json:"heartbeatSeconds"`
}

type WorkerHeartbeat struct {
	InputReceipts   []liveInputReceipt      `json:"inputReceipts,omitempty"`
	Leases          []state.AssignmentLease `json:"leases,omitempty"`
	Draining        bool                    `json:"draining,omitempty"`
	InstanceID      string                  `json:"instanceId,omitempty"`
	ProtocolVersion string                  `json:"protocolVersion"`
	Active          []string                `json:"activeAssignments,omitempty"`
	Capacity        *int                    `json:"capacity,omitempty"`
	AvailableSlots  int                     `json:"availableSlots,omitempty"`
}

type WorkerHeartbeatResult struct {
	CoordinatorEpoch int64 `json:"coordinatorEpoch"`
	LeaseSeconds     int   `json:"leaseSeconds"`
	Commands         []any `json:"commands,omitempty"`
}

// WorkerAssignment carries an opaque generic execute request and fencing data.
type WorkerAssignment struct {
	AssignmentID     string          `json:"assignmentId"`
	RunID            string          `json:"runId"`
	ActivationID     string          `json:"activationId"`
	AttemptID        string          `json:"attemptId"`
	CoordinatorEpoch int64           `json:"coordinatorEpoch"`
	Generation       int64           `json:"generation"`
	LeaseUntil       string          `json:"leaseUntil"`
	Request          json.RawMessage `json:"request"`
}

type WorkerCompletion struct {
	RemoteAccounting    *contract.RemoteAccountingUpdate `json:"remoteAccounting,omitempty"`
	OutputErrors        map[string]string                `json:"outputErrors,omitempty"`
	AcknowledgedInputs  []string                         `json:"acknowledgedInputs,omitempty"`
	AssignmentID        string                           `json:"assignmentId"`
	RunID               string                           `json:"runId"`
	ActivationID        string                           `json:"activationId"`
	AttemptID           string                           `json:"attemptId"`
	CoordinatorEpoch    int64                            `json:"coordinatorEpoch"`
	Generation          int64                            `json:"generation"`
	State               string                           `json:"state"`
	Outputs             map[string]any                   `json:"outputs,omitempty"`
	Error               *WorkerError                     `json:"error,omitempty"`
	LogCursor           string                           `json:"logCursor,omitempty"`
	Logs                string                           `json:"logs,omitempty"`
	Accounting          *contract.Accounting             `json:"accounting,omitempty"`
	Session             *contract.SessionHandle          `json:"session,omitempty"`
	SessionID           string                           `json:"sessionId,omitempty"`
	Reconciliation      *contract.Reconciliation         `json:"reconciliation,omitempty"`
	RemoteTask          *contract.RemoteTaskReference    `json:"remoteTask,omitempty"`
	ExecutionDurationMS *int64                           `json:"executionDurationMs,omitempty"`
}

// WorkerLogAppend carries one incremental log chunk for an active assignment.
type WorkerLogAppend struct {
	Snapshot         *state.ArtifactSnapshot `json:"snapshot,omitempty"`
	AssignmentID     string                  `json:"assignmentId"`
	RunID            string                  `json:"runId"`
	ActivationID     string                  `json:"activationId"`
	AttemptID        string                  `json:"attemptId"`
	CoordinatorEpoch int64                   `json:"coordinatorEpoch"`
	Generation       int64                   `json:"generation"`
	Stream           string                  `json:"stream"`
	Seq              int                     `json:"seq"`
	Data             []byte                  `json:"data"`
}

type WorkerAccountingAppend struct {
	AssignmentID     string              `json:"assignmentId"`
	RunID            string              `json:"runId"`
	ActivationID     string              `json:"activationId"`
	AttemptID        string              `json:"attemptId"`
	CoordinatorEpoch int64               `json:"coordinatorEpoch"`
	Generation       int64               `json:"generation"`
	Accounting       contract.Accounting `json:"accounting"`
}

type WorkerError struct {
	Diagnostic *diagnostic.Diagnostic `json:"diagnostic,omitempty"`
	Code       string                 `json:"code"`
	Message    string                 `json:"message"`
	Retryable  bool                   `json:"retryable,omitempty"`
}

// WorkerProtocolHandler serves the coordinator-facing worker API.
type WorkerProtocolHandler struct {
	backend WorkerProtocolBackend
}

func NewWorkerProtocolHandler(backend WorkerProtocolBackend) http.Handler {
	h := &WorkerProtocolHandler{backend: backend}
	return http.HandlerFunc(h.serveHTTP)
}

func (h *WorkerProtocolHandler) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if h.backend == nil {
		writeError(w, http.StatusServiceUnavailable, CodeWorkerProtocolError, "worker protocol is not configured")
		return
	}
	if r.URL.Path == "/v1/workers/register" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
			return
		}
		var req WorkerRegistration
		if !decodeWorkerJSON(w, r, &req) {
			return
		}
		if len(req.SupportedProtocolVersions) > 0 {
			req.ProtocolVersion = ""
			for _, version := range req.SupportedProtocolVersions {
				if version == WorkerProtocolVersion {
					req.ProtocolVersion = version
					break
				}
			}
		}
		if req.ProtocolVersion != WorkerProtocolVersion {
			writeError(w, http.StatusBadRequest, CodeUnsupportedProtocol, "protocolVersion must be v1")
			return
		}
		if req.WorkerID == "" || req.Capacity < 1 {
			writeError(w, http.StatusBadRequest, CodeInvalidRegistration, "workerId and positive capacity are required")
			return
		}
		if err := validateCapabilities(req.Capabilities); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRegistration, err.Error())
			return
		}
		result, err := h.backend.Register(req)
		if err != nil {
			writeWorkerBackendError(w, err)
			return
		}
		result.ProtocolVersion = req.ProtocolVersion
		writeJSON(w, http.StatusOK, result)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/workers/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/workers/"), "/")
		workerID := ""
		if len(parts) > 0 {
			workerID = parts[0]
		}
		if len(parts) == 2 && parts[1] == "heartbeat" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
				return
			}
			var req WorkerHeartbeat
			if !decodeWorkerJSON(w, r, &req) {
				return
			}
			if req.ProtocolVersion != WorkerProtocolVersion {
				writeError(w, http.StatusBadRequest, CodeUnsupportedProtocol, "protocolVersion must be v1")
				return
			}
			if req.Capacity != nil && *req.Capacity < 0 {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "capacity must be non-negative")
				return
			}
			result, err := h.backend.Heartbeat(workerID, req)
			if err != nil {
				writeWorkerBackendError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		if len(parts) == 2 && parts[1] == "assignments" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			var assignment *WorkerAssignment
			var err error
			if backend, ok := h.backend.(interface {
				NextAssignmentForInstance(string, string) (*WorkerAssignment, error)
			}); ok {
				assignment, err = backend.NextAssignmentForInstance(workerID, r.URL.Query().Get("instanceId"))
			} else {
				assignment, err = h.backend.NextAssignment(workerID)
			}
			if err != nil {
				writeWorkerBackendError(w, err)
				return
			}
			if assignment == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if assignment.AssignmentID == "" || assignment.RunID == "" || assignment.ActivationID == "" || assignment.AttemptID == "" || assignment.CoordinatorEpoch < 1 || assignment.Generation < 1 || !validLeaseTime(assignment.LeaseUntil) || !json.Valid(assignment.Request) {
				writeError(w, http.StatusInternalServerError, CodeCoordinatorError, "coordinator returned an invalid assignment")
				return
			}
			writeJSON(w, http.StatusOK, assignment)
			return
		}
		if len(parts) == 3 && parts[1] == "assignments" && parts[2] == "log" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
				return
			}
			var req WorkerLogAppend
			if !decodeWorkerJSON(w, r, &req) {
				return
			}
			if req.AssignmentID == "" || req.Generation < 1 || req.CoordinatorEpoch < 1 || req.RunID == "" || req.ActivationID == "" || req.AttemptID == "" || req.Stream == "" || req.Seq < 1 || (len(req.Data) == 0 && req.Snapshot == nil) {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "assignment identity, positive epoch and generation, stream, seq, and data are required")
				return
			}
			if len(req.Data) > 64<<10 || len(req.Stream) > 256 {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "log chunk exceeds 64 KiB")
				return
			}
			if err := h.backend.AppendWorkerLog(workerID, req); err != nil {
				writeWorkerBackendError(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
			return
		}
		if len(parts) == 3 && parts[1] == "assignments" && parts[2] == "accounting" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
				return
			}
			var req WorkerAccountingAppend
			if !decodeWorkerJSON(w, r, &req) {
				return
			}
			if req.AssignmentID == "" || req.Generation < 1 || req.CoordinatorEpoch < 1 || req.RunID == "" || req.ActivationID == "" || req.AttemptID == "" {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "assignment identity, positive epoch and generation are required")
				return
			}
			if err := req.Accounting.Validate(); err != nil {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid accounting snapshot")
				return
			}
			raw, err := contract.CanonicalAccounting(req.Accounting)
			if err != nil {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid accounting snapshot")
				return
			}
			if len(raw) > 64<<10 {
				writeError(w, http.StatusRequestEntityTooLarge, CodeBadRequest, "accounting snapshot exceeds 64 KiB")
				return
			}
			if err := h.backend.RecordWorkerAccounting(workerID, req); err != nil {
				writeWorkerBackendError(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "revision": req.Accounting.Revision})
			return
		}
		if len(parts) == 3 && parts[1] == "assignments" && parts[2] == "complete" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
				return
			}
			var req WorkerCompletion
			if !decodeWorkerJSON(w, r, &req) {
				return
			}
			if req.AssignmentID == "" || req.Generation < 1 || req.CoordinatorEpoch < 1 || req.RunID == "" || req.ActivationID == "" || req.AttemptID == "" || (req.State != "succeeded" && req.State != "failed" && req.State != "cancelled") {
				writeError(w, http.StatusBadRequest, CodeInvalidCompletion, "assignment identity, positive coordinator epoch and generation, and terminal state are required")
				return
			}
			if err := h.backend.Complete(workerID, req); err != nil {
				writeWorkerBackendError(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
			return
		}
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET or POST")
		return
	}
	writeError(w, http.StatusNotFound, CodeNotFound, "unknown worker protocol endpoint")
}

func validLeaseTime(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func decodeWorkerJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, "request body must contain one JSON value")
		return false
	}
	return true
}

func validateCapabilities(capabilities []WorkerCapability) error {
	seen := make(map[string]bool)
	for _, c := range capabilities {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return errors.New("capability name must not be empty")
		}
		if name != c.Name {
			return errors.New("capability name must not have surrounding whitespace")
		}
		if seen[name] {
			return errors.New("capability names must be unique")
		}
		seen[name] = true
	}
	return nil
}

type workerProtocolError interface{ ProtocolError() (int, string, string) }

func writeWorkerBackendError(w http.ResponseWriter, err error) {
	var e workerProtocolError
	if errors.As(err, &e) {
		status, code, message := e.ProtocolError()
		if status >= 400 && status <= 599 && code != "" && message != "" {
			writeError(w, status, code, message)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, CodeCoordinatorError, "coordinator could not process worker request")
}
