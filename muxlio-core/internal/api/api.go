// Package api implements the Muxlio HTTP API for the M1 coordinator.
//
// Endpoints:
//
//	GET  /v1/runs                   list all runs
//	POST /v1/runs                   submit and start a run
//	GET  /v1/runs/:id               get a single run
//	GET  /v1/runs/:id/events        paginated event log (after=, limit=)
//	GET  /v1/runs/:id/activations   materialized activations
//	GET  /v1/runs/:id/graph         materialized scopes and activations
//	GET  /v1/runs/:id/logs          worker logs
//	POST /v1/runs/:id/signals/:name deliver a signal to a waiting run
//	GET  /v1/workers                active worker assignments and live registered workers
//	GET  /healthz                   liveness probe
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

// Runner creates and executes runs submitted through the API.
type Runner interface {
	CreateRun(input json.RawMessage) (string, error)
	Run(runID string) error
}

type planRunner interface {
	CreatePlanRun(p *plan.Plan, input json.RawMessage) (string, error)
}

type runController interface {
	ControlRun(runID, action string) error
}

// NewRouter builds and returns the HTTP mux for the coordinator API.
func NewRouter(store state.Store, runner Runner, workerProtocol ...http.Handler) http.Handler {
	return newRouter(store, runner, nil, workerProtocol...)
}

// NewRouterWithArtifacts builds the coordinator API with an artifact driver.
func NewRouterWithArtifacts(store state.Store, runner Runner, driver artifacts.Driver, workerProtocol ...http.Handler) http.Handler {
	return newRouter(store, runner, driver, workerProtocol...)
}

func newRouter(store state.Store, runner Runner, artifactDriver artifacts.Driver, workerProtocol ...http.Handler) http.Handler {
	mux := http.NewServeMux()
	if artifactDriver != nil {
		planner := artifacts.PlannerFromDriver(artifactDriver)
		mux.HandleFunc("/v1/artifacts/", func(w http.ResponseWriter, r *http.Request) {
			remainder := strings.TrimPrefix(r.URL.Path, "/v1/artifacts/")
			if remainder == "" || strings.Count(remainder, "/") > 1 {
				writeError(w, http.StatusBadRequest, CodeInvalidArtifactDigest, "invalid artifact digest")
				return
			}
			parts := strings.SplitN(remainder, "/", 2)
			digest := parts[0]
			sub := ""
			if len(parts) > 1 {
				sub = parts[1]
			}
			if sub == "transfer" {
				if r.Method != http.MethodGet {
					w.Header().Set("Allow", http.MethodGet)
					writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
					return
				}
				method := r.URL.Query().Get("method")
				if method == "" {
					method = http.MethodGet
				}
				var size int64
				if raw := r.URL.Query().Get("size"); raw != "" {
					var err error
					size, err = strconv.ParseInt(raw, 10, 64)
					if err != nil || size < 0 {
						writeError(w, http.StatusBadRequest, CodeBadRequest, "size must be a non-negative integer")
						return
					}
				}
				plan, err := planner.Plan(r.Context(), digest, method, size, nil)
				if err != nil {
					status := http.StatusBadRequest
					if errors.Is(err, artifacts.ErrInvalidDigest) {
						status = http.StatusBadRequest
					}
					writeError(w, status, CodeArtifactDownloadFailed, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, plan)
				return
			}
			if sub != "" {
				writeError(w, http.StatusNotFound, CodeNotFound, "unknown artifact endpoint")
				return
			}
			switch r.Method {
			case http.MethodPut:
				expected := r.Header.Get("X-Artifact-SHA256")
				if expected != "" && expected != digest {
					writeError(w, http.StatusUnprocessableEntity, CodeDigestMismatch, "header digest does not match URL digest")
					return
				}
				ref, err := artifactDriver.PutStream(r.Context(), r.Body, r.Header.Get("Content-Type"), digest)
				if err != nil {
					status := http.StatusBadRequest
					if errors.Is(err, artifacts.ErrIntegrity) || errors.Is(err, artifacts.ErrInvalidDigest) {
						status = http.StatusUnprocessableEntity
					}
					writeError(w, status, CodeArtifactUploadFailed, err.Error())
					return
				}
				writeJSON(w, http.StatusCreated, ref)
			case http.MethodGet:
				stream, ref, err := artifactDriver.GetStream(r.Context(), digest)
				if err != nil {
					status := http.StatusInternalServerError
					if errors.Is(err, artifacts.ErrNotFound) {
						status = http.StatusNotFound
					} else if errors.Is(err, artifacts.ErrInvalidDigest) {
						status = http.StatusBadRequest
					}
					writeError(w, status, CodeArtifactDownloadFailed, err.Error())
					return
				}
				defer stream.Close()
				w.Header().Set("Content-Length", strconv.FormatInt(ref.Size, 10))
				w.Header().Set("X-Artifact-SHA256", ref.Digest)
				if ref.Mime != "" {
					w.Header().Set("Content-Type", ref.Mime)
				}
				_, _ = io.Copy(w, stream)
			case http.MethodHead:
				stream, ref, err := artifactDriver.GetStream(r.Context(), digest)
				if err != nil {
					status := http.StatusInternalServerError
					if errors.Is(err, artifacts.ErrNotFound) {
						status = http.StatusNotFound
					} else if errors.Is(err, artifacts.ErrInvalidDigest) {
						status = http.StatusBadRequest
					}
					writeError(w, status, CodeArtifactDownloadFailed, err.Error())
					return
				}
				_ = stream.Close()
				w.Header().Set("Content-Length", strconv.FormatInt(ref.Size, 10))
				w.Header().Set("X-Artifact-SHA256", ref.Digest)
				if ref.Mime != "" {
					w.Header().Set("Content-Type", ref.Mime)
				}
				w.WriteHeader(http.StatusOK)
			default:
				w.Header().Set("Allow", "GET, HEAD, PUT")
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET, HEAD, or PUT")
			}
		})
	}
	if len(workerProtocol) > 0 && workerProtocol[0] != nil {
		mux.Handle("/v1/workers/", workerProtocol[0])
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Workflow Versions registry (DESIGN.md 8.2)
	mux.HandleFunc("/v1/workflow-versions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list, err := store.ListWorkflowVersions()
			if err != nil {
				writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"workflowVersions": list})
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET or POST")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
			return
		}
		var p plan.Plan
		if err := json.Unmarshal(data, &p); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, "invalid plan JSON: "+err.Error())
			return
		}
		if errs := plan.Validate(&p); plan.HasErrors(errs) {
			writeValidationErrors(w, plan.ErrorsOnly(errs))
			return
		}
		if err := p.VerifyDigest(); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
			return
		}
		if p.Digest == "" {
			d, err := p.ComputeDigest()
			if err != nil {
				writeError(w, http.StatusBadRequest, CodeInvalidJSON, "computing digest: "+err.Error())
				return
			}
			p.Digest = d
		}
		// Persist the verified plan with its digest in a consistent encoding so
		// whitespace and an omitted digest do not conflict with an existing pin.
		data, err = json.Marshal(&p)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
			return
		}
		if err := store.SaveWorkflowVersion(p.Digest, data); err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"digest": p.Digest, "name": p.Name})
	})

	mux.HandleFunc("/v1/workflow-versions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
			return
		}
		digest := strings.TrimPrefix(r.URL.Path, "/v1/workflow-versions/")
		if digest == "" {
			writeError(w, http.StatusBadRequest, CodeInvalidJSON, "missing digest")
			return
		}
		raw, err := store.GetWorkflowVersion(digest)
		if err != nil {
			writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})

	mux.HandleFunc("/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleCreateRun(w, r, runner)
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET or POST")
			return
		}
		runs, err := store.ListRuns()
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	})

	mux.HandleFunc("/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		// strip prefix and split path
		path := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
		parts := strings.SplitN(path, "/", 3)
		runID := parts[0]
		if runID == "" {
			writeError(w, http.StatusBadRequest, CodeMissingRunID, "missing run id")
			return
		}

		// POST /v1/runs/:id/signals/:name
		if len(parts) == 3 && parts[1] == "signals" {
			signalName := parts[2]
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
				return
			}
			handleSendSignal(w, r, store, runID, signalName)
			return
		}

		// POST /v1/runs/:id/{pause,resume,cancel}
		if len(parts) == 2 && (parts[1] == "pause" || parts[1] == "resume" || parts[1] == "cancel" || parts[1] == "retry") {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use POST")
				return
			}
			controller, ok := runner.(runController)
			if !ok {
				writeError(w, http.StatusServiceUnavailable, CodeRunControlUnavailable, "run controls are unavailable")
				return
			}
			if err := controller.ControlRun(runID, parts[1]); err != nil {
				status := http.StatusConflict
				if strings.Contains(err.Error(), "not found") {
					status = http.StatusNotFound
				}
				writeError(w, status, CodeRunControlRejected, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runId": runID, "action": parts[1]})
			return
		}

		// GET /v1/runs/:id/events, /events/stream, and /stream (SSE live stream)
		if len(parts) == 2 && parts[1] == "snapshots" {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			if _, err := store.GetRun(runID); err != nil {
				writeError(w, http.StatusNotFound, CodeNotFound, "run not found")
				return
			}
			snapshots, err := state.ListSnapshots(store, runID, r.URL.Query().Get("activationId"), r.URL.Query().Get("attemptId"), r.URL.Query().Get("current") == "true")
			if err != nil {
				writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
				return
			}
			after, _ := strconv.Atoi(r.URL.Query().Get("after"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit <= 0 || limit > 200 {
				limit = 200
			}
			page := make([]state.Event, 0)
			for _, event := range snapshots {
				if event.Seq > after {
					page = append(page, event)
				}
			}
			more := len(page) > limit
			if more {
				page = page[:limit]
			}
			next := after
			if len(page) > 0 {
				next = page[len(page)-1].Seq
			}
			writeJSON(w, http.StatusOK, map[string]any{"snapshots": page, "nextAfter": next, "hasMore": more})
			return
		}
		if len(parts) == 2 && parts[1] == "stream" {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			handleStreamEvents(w, r, store, runID)
			return
		}
		if (len(parts) == 2 && parts[1] == "events") || (len(parts) == 3 && parts[1] == "events" && parts[2] == "stream") {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			if len(parts) == 3 {
				handleStreamEvents(w, r, store, runID)
				return
			}
			handleGetEvents(w, r, store, runID)
			return
		}
		if len(parts) == 2 && parts[1] == "activations" {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			handleGetActivations(w, store, runID)
			return
		}
		if len(parts) == 2 && parts[1] == "attempts" {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			handleGetAttempts(w, r, store, runID)
			return
		}
		if len(parts) == 2 && parts[1] == "graph" {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			handleGetGraph(w, store, runID)
			return
		}
		if len(parts) == 2 && parts[1] == "logs" {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			handleGetLogs(w, r, store, runID)
			return
		}

		// GET /v1/runs/:id
		if len(parts) == 1 {
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
				return
			}
			handleGetRun(w, r, store, runID)
			return
		}

		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("unknown path /v1/runs/%s", path))
	})

	mux.HandleFunc("/v1/workers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "use GET")
			return
		}
		runs, err := store.ListRuns()
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
			return
		}
		assignments := make([]state.Assignment, 0)
		for _, run := range runs {
			runAssignments, err := store.ListAssignments(run.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
				return
			}
			for _, assignment := range runAssignments {
				if assignment.State == "active" {
					assignments = append(assignments, assignment)
				}
			}
		}
		registered := make([]state.Worker, 0)
		if registry, ok := store.(workerRegistry); ok {
			registered, err = registry.ListWorkers(time.Now().Add(-WorkerHeartbeatWindow))
			if err != nil {
				writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
				return
			}
			if registered == nil {
				registered = []state.Worker{}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"workers": assignments, "registeredWorkers": registered})
	})

	return mux
}

type workerRegistry interface {
	ListWorkers(time.Time) ([]state.Worker, error)
}

func handleGetAttempts(w http.ResponseWriter, r *http.Request, store state.Store, runID string) {
	if _, err := store.GetRun(runID); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	activationID := r.URL.Query().Get("activationId")
	if activationID != "" {
		a, err := store.GetActivation(activationID)
		if err != nil || a.RunID != runID {
			writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("activation %q not found for run %q", activationID, runID))
			return
		}
	}
	attempts, err := store.ListExecutionAttempts(runID, activationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempts": attempts})
}

func handleCreateRun(w http.ResponseWriter, r *http.Request, runner Runner) {
	var request struct {
		Plan  plan.Plan       `json:"plan"`
		Input json.RawMessage `json:"input"`
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, CodeInvalidJSON, "request body must contain one JSON object")
		return
	}
	if errs := plan.Validate(&request.Plan); plan.HasErrors(errs) {
		writeValidationErrors(w, plan.ErrorsOnly(errs))
		return
	}
	if runner == nil {
		writeError(w, http.StatusInternalServerError, CodeRunnerUnavailable, "run execution is unavailable")
		return
	}
	var runID string
	var err error
	if planRunner, ok := runner.(planRunner); ok {
		runID, err = planRunner.CreatePlanRun(&request.Plan, request.Input)
	} else {
		runID, err = runner.CreateRun(request.Input)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeRunError, err.Error())
		return
	}
	go func() { _ = runner.Run(runID) }()
	writeJSON(w, http.StatusCreated, map[string]string{"runId": runID, "state": plan.RunRunning})
}

func handleGetRun(w http.ResponseWriter, r *http.Request, store state.Store, runID string) {
	run, err := store.GetRun(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func handleGetEvents(w http.ResponseWriter, r *http.Request, store state.Store, runID string) {
	after := 0
	limit := 100
	if v := r.URL.Query().Get("after"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			after = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if _, err := store.GetRun(runID); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	events, err := store.GetEvents(runID, after, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	nextAfter := after
	if len(events) > 0 {
		nextAfter = events[len(events)-1].Seq
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "after": after, "limit": limit, "nextAfter": nextAfter, "hasMore": hasMore})
}

func handleStreamEvents(w http.ResponseWriter, r *http.Request, store state.Store, runID string) {
	if _, err := store.GetRun(runID); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	cursor := r.URL.Query().Get("after")
	if cursor == "" {
		cursor = r.Header.Get("Last-Event-ID")
	}
	after := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, CodeBadEventCursor, "event cursor must be a non-negative sequence number")
			return
		}
		after = n
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, CodeStreamUnsupported, "streaming responses are unavailable")
		return
	}
	responseController := http.NewResponseController(w)
	setWriteDeadline := func() { _ = responseController.SetWriteDeadline(time.Now().Add(10 * time.Second)) }
	setWriteDeadline()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := store.GetEvents(runID, after, 100)
		if err != nil {
			return
		}
		for _, event := range events {
			payload, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, payload); err != nil {
				return
			}
			after = event.Seq
		}
		flusher.Flush()
		setWriteDeadline()
		if len(events) == 100 {
			continue
		}
		run, err := store.GetRun(runID)
		if err != nil || isTerminalRun(run.State) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if len(events) == 0 {
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()
				setWriteDeadline()
			}
		}
	}
}

func isTerminalRun(state string) bool {
	return state == plan.RunSucceeded || state == plan.RunFailed || state == plan.RunCancelled
}

func handleGetActivations(w http.ResponseWriter, store state.Store, runID string) {
	if _, err := store.GetRun(runID); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	activations, err := store.ListActivations(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activations": activations})
}

func handleGetGraph(w http.ResponseWriter, store state.Store, runID string) {
	if _, err := store.GetRun(runID); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	activations, err := store.ListActivations(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	type scope struct {
		Path []string `json:"path"`
	}
	scopes := make([]scope, 0)
	seen := map[string]bool{}
	for _, a := range activations {
		for i := 1; i <= len(a.ScopePath); i++ {
			path := append([]string(nil), a.ScopePath[:i]...)
			key, _ := json.Marshal(path)
			if !seen[string(key)] {
				seen[string(key)] = true
				scopes = append(scopes, scope{Path: path})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": scopes, "activations": activations})
}

func handleGetLogs(w http.ResponseWriter, r *http.Request, store state.Store, runID string) {
	if _, err := store.GetRun(runID); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	activationID := r.URL.Query().Get("activationId")
	if activationID != "" {
		a, err := store.GetActivation(activationID)
		if err != nil || a.RunID != runID {
			writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("activation %q not found for run %q", activationID, runID))
			return
		}
	}
	logs, err := store.GetLogChunks(runID, activationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func handleSendSignal(w http.ResponseWriter, r *http.Request, store state.Store, runID, name string) {
	var payload map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&payload)
	}
	ikey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if err := store.SendSignal(runID, name, payload, ikey); err != nil {
		writeError(w, http.StatusInternalServerError, CodeStoreError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runId": runID, "signal": name})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a structured APIError envelope. errCode must be one of
// the MUXLIO_* constants defined in errors.go; the fix is looked up
// automatically and may be overridden by callers that pass additional details.
func writeError(w http.ResponseWriter, httpStatus int, errCode, message string) {
	env := APIError{Code: errCode, Message: message, Fix: codeFix[errCode]}
	writeJSON(w, httpStatus, env)
}

// writeValidationErrors writes a 422 response whose details[] array contains
// one ErrorDetail per plan validation error.
func writeValidationErrors(w http.ResponseWriter, errs []plan.ValidationError) {
	details := make([]ErrorDetail, len(errs))
	for i, e := range errs {
		details[i] = ErrorDetail{
			Code:     e.Code,
			Severity: e.Severity,
			Phase:    e.Phase,
			Subgraph: e.Subgraph,
			NodeID:   e.NodeID,
			Message:  e.Message,
			Fix:      e.Fix,
		}
	}
	env := APIError{
		Code:    CodeValidationError,
		Message: fmt.Sprintf("plan has %d validation error(s)", len(errs)),
		Fix:     codeFix[CodeValidationError],
		Details: details,
	}
	writeJSON(w, http.StatusUnprocessableEntity, env)
}
