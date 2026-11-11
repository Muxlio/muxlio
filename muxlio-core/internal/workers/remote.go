package workers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/telemetry"
)

const remoteProtocolVersion = "v1"

type RemoteCapability = state.WorkerCapability
type remoteRegistration struct {
	SupportedProtocolVersions []string           `json:"supportedProtocolVersions,omitempty"`
	ProtocolVersion           string             `json:"protocolVersion"`
	WorkerID                  string             `json:"workerId"`
	InstanceID                string             `json:"instanceId,omitempty"`
	OS                        string             `json:"os,omitempty"`
	Architecture              string             `json:"architecture,omitempty"`
	Capacity                  int                `json:"capacity"`
	Capabilities              []RemoteCapability `json:"capabilities"`
	Labels                    map[string]string  `json:"labels,omitempty"`
}
type remoteRegistrationResult struct {
	ProtocolVersion  string `json:"protocolVersion,omitempty"`
	CoordinatorEpoch int64  `json:"coordinatorEpoch"`
	HeartbeatSeconds int    `json:"heartbeatSeconds"`
}
type remoteInputReceipt struct {
	AssignmentID       string   `json:"assignmentId"`
	Generation         int64    `json:"generation"`
	CoordinatorEpoch   int64    `json:"coordinatorEpoch"`
	AcknowledgedInputs []string `json:"acknowledgedInputs"`
}

type remoteHeartbeat struct {
	InputReceipts   []remoteInputReceipt    `json:"inputReceipts,omitempty"`
	Leases          []state.AssignmentLease `json:"leases,omitempty"`
	Draining        bool                    `json:"draining,omitempty"`
	InstanceID      string                  `json:"instanceId,omitempty"`
	ProtocolVersion string                  `json:"protocolVersion"`
	Active          []string                `json:"activeAssignments,omitempty"`
	Capacity        *int                    `json:"capacity,omitempty"`
	AvailableSlots  int                     `json:"availableSlots"`
}
type remoteHeartbeatResult struct {
	CoordinatorEpoch int64             `json:"coordinatorEpoch"`
	LeaseSeconds     int               `json:"leaseSeconds"`
	Commands         []json.RawMessage `json:"commands,omitempty"`
}
type remoteAssignment struct {
	AssignmentID     string          `json:"assignmentId"`
	RunID            string          `json:"runId"`
	ActivationID     string          `json:"activationId"`
	AttemptID        string          `json:"attemptId"`
	CoordinatorEpoch int64           `json:"coordinatorEpoch"`
	Generation       int64           `json:"generation"`
	LeaseUntil       string          `json:"leaseUntil"`
	Request          json.RawMessage `json:"request"`
}
type remoteError struct {
	Diagnostic *diagnostic.Diagnostic `json:"diagnostic,omitempty"`
	Code       string                 `json:"code"`
	Message    string                 `json:"message"`
	Retryable  bool                   `json:"retryable,omitempty"`
}
type remoteCompletion struct {
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
	Error               *remoteError                     `json:"error,omitempty"`
	Logs                string                           `json:"logs,omitempty"`
	Accounting          *contract.Accounting             `json:"accounting,omitempty"`
	Session             *contract.SessionHandle          `json:"session,omitempty"`
	SessionID           string                           `json:"sessionId,omitempty"`
	Reconciliation      *contract.Reconciliation         `json:"reconciliation,omitempty"`
	RemoteTask          *contract.RemoteTaskReference    `json:"remoteTask,omitempty"`
	ExecutionDurationMS *int64                           `json:"executionDurationMs,omitempty"`
}

// RemoteWorkerConfig describes an outbound worker connection.
type RemoteWorkerConfig struct {
	CoordinatorURL    string
	WorkerID          string
	Token             string
	Capacity          int
	Capabilities      []RemoteCapability
	Labels            map[string]string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	HTTPClient        *http.Client
	WorkBase          string
	Workspace         string
	// CapacityLimit optionally supplies a live admission limit (zero drains).
	// It is read by the supervisor, never by executor goroutines.
	CapacityLimit func() int
	// Drain stops admission, renews occupied slots, and exits after receipts.
	Drain <-chan struct{}
	// ShutdownGracePeriod allows committed work to finish after ctx cancellation.
	ShutdownGracePeriod time.Duration
	// ExecutorFactory creates a runner owned by one attempt. Without a factory,
	// executors must support concurrent Execute calls; attempt-aware executors
	// can implement NewAttempt instead.
	ExecutorFactory func() Executor
}

// RunRemoteWorker registers and polls until cancellation or drain. With a
// shutdown grace period, cancellation drains admitted assignments first.
func RunRemoteWorker(ctx context.Context, cfg RemoteWorkerConfig, executor Executor) error {
	if cfg.WorkerID == "" || cfg.CoordinatorURL == "" || (executor == nil && cfg.ExecutorFactory == nil) {
		return fmt.Errorf("worker id, coordinator URL, and executor are required")
	}
	parent := ctx
	shutdown := parent.Done()
	ctx, stop := context.WithCancel(ctx)
	var children sync.WaitGroup
	defer func() { stop(); children.Wait() }()
	base, err := url.Parse(strings.TrimRight(cfg.CoordinatorURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return fmt.Errorf("invalid coordinator URL")
	}
	if cfg.Capacity < 1 {
		cfg.Capacity = 1
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 5 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second}
	}
	callWithTimeout := func(method, path string, body any, target any, requestTimeout time.Duration) (int, error) {
		var reader io.Reader
		if body != nil {
			raw, e := json.Marshal(body)
			if e != nil {
				return 0, e
			}
			reader = bytes.NewReader(raw)
		}

		// Create a timeout context for this specific request
		reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()

		req, e := http.NewRequestWithContext(reqCtx, method, base.String()+path, reader)
		if e != nil {
			return 0, e
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
		resp, e := client.Do(req)
		if e != nil {
			return 0, e
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return resp.StatusCode, fmt.Errorf("coordinator returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		if target != nil && resp.StatusCode != http.StatusNoContent {
			if e = json.NewDecoder(resp.Body).Decode(target); e != nil {
				return resp.StatusCode, e
			}
		}
		return resp.StatusCode, nil
	}
	instance := make([]byte, 16)
	if _, err := rand.Read(instance); err != nil {
		return err
	}
	if script, ok := executor.(*ScriptExecutor); ok {
		script.WorkerID = cfg.WorkerID
		labels := make(map[string]string, len(cfg.Labels)+1)
		for key, value := range cfg.Labels {
			labels[key] = value
		}
		labels[SessionEnvironmentLabel] = script.SessionEnvironment()
		cfg.Labels = labels
	}
	registration := remoteRegistration{InstanceID: fmt.Sprintf("%x", instance), ProtocolVersion: remoteProtocolVersion, WorkerID: cfg.WorkerID, OS: runtime.GOOS, Architecture: runtime.GOARCH, Capacity: cfg.Capacity, Capabilities: cfg.Capabilities, Labels: cfg.Labels}
	registration.SupportedProtocolVersions = []string{remoteProtocolVersion}
	var registered remoteRegistrationResult

	// Helper function with retry logic for coordinator calls
	call := func(method, path string, body any, target any) (int, error) {
		return callWithTimeout(method, path, body, target, 30*time.Second)
	}

	retryableCall := func(method, path string, body any, target any) (int, error) {
		backoff := time.Second
		maxBackoff := 30 * time.Second

		for attempt := 0; ; attempt++ {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}

			if attempt > 0 {
				select {
				case <-ctx.Done():
					return 0, ctx.Err()
				case <-time.After(backoff):
					backoff = time.Duration(float64(backoff) * 1.5)
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}

			status, err := call(method, path, body, target)
			if err == nil {
				return status, nil
			}

			// Check if error is retryable (network error or 5xx)
			if status >= 500 || status == 0 {
				// Network error or server error - retryable
				continue
			}

			if status == 409 && !strings.HasSuffix(path, "/complete") {
				var result remoteRegistrationResult
				_, _ = call(http.MethodPost, "/v1/workers/register", registration, &result)
				continue
			}
			// 4xx errors are not retryable (except 429)
			if status == 429 {
				// Rate limited - retryable
				continue
			}

			// Other 4xx errors are not retryable
			return status, err
		}

	}

	if _, err := retryableCall(http.MethodPost, "/v1/workers/register", registration, &registered); err != nil {
		return err
	}
	if registered.ProtocolVersion != "" && registered.ProtocolVersion != remoteProtocolVersion {
		return fmt.Errorf("coordinator selected unsupported worker protocol %q", registered.ProtocolVersion)
	}
	// Registration remains cancellable. Once registered, signal cancellation
	// initiates drain while requests and children retain a live grace context.
	if cfg.ShutdownGracePeriod > 0 {
		stop()
		graceCtx, graceStop := context.WithCancel(context.WithoutCancel(parent))
		defer graceStop()
		ctx = graceCtx
	}

	type attempt struct {
		inputContext   context.Context
		assignment     remoteAssignment
		cancel         context.CancelFunc
		revoked        bool
		leaseUntil     time.Time
		leaseRenewedAt time.Time
	}
	type execution struct {
		id      string
		result  ExecuteResult
		err     error
		secrets map[string]string
	}
	type receipt struct {
		id     string
		status int
		err    error
	}
	type pollResult struct {
		assignment remoteAssignment
		status     int
		err        error
	}
	active := make(map[string]*attempt)
	type inputResult struct {
		id      string
		receipt remoteInputReceipt
	}
	inputResults := make(chan inputResult, cfg.Capacity)
	inputBusy := make(map[string]bool)
	pendingInputReceipts := []remoteInputReceipt{}
	done := make(chan execution, cfg.Capacity)
	receipts := make(chan receipt, cfg.Capacity)
	polled := make(chan pollResult, 1)
	polling := false
	limit := cfg.Capacity
	draining := false
	drain := cfg.Drain
	var shutdownDeadline <-chan time.Time
	var shutdownTimer *time.Timer
	defer func() {
		if shutdownTimer != nil {
			shutdownTimer.Stop()
		}
	}()
	updateLimit := func() {
		if draining {
			limit = 0
			return
		}
		if cfg.CapacityLimit != nil {
			limit = cfg.CapacityLimit()
			if limit < 0 {
				limit = 0
			}
		}
	}
	launch := func(assignment remoteAssignment) error {
		var err error
		// Preparation failures belong to this attempt, just like adapter errors.
		// They must not terminate unrelated children already admitted here.
		reject := func(err error) error {
			_, cancel := context.WithCancel(ctx)
			leaseUntil, _ := time.Parse(time.RFC3339Nano, assignment.LeaseUntil)
			active[assignment.AssignmentID] = &attempt{assignment: assignment, cancel: cancel, leaseUntil: leaseUntil, leaseRenewedAt: time.Now()}
			children.Add(1)
			go func() {
				defer children.Done()
				defer cancel()
				select {
				case done <- execution{id: assignment.AssignmentID, err: err}:
				case <-ctx.Done():
				}
			}()
			return nil
		}
		var executeReq ExecuteRequest
		if err := contract.DecodeJSON(assignment.Request, &executeReq); err != nil {
			return reject(fmt.Errorf("invalid assignment request: %w", err))
		}
		executeReq.RunID, executeReq.ActivationID, executeReq.AttemptID = assignment.RunID, assignment.ActivationID, assignment.AttemptID
		executeReq.resourcesAdmitted = true
		executeReq, err = ApplyWorkerWorkspace(executeReq, cfg.Workspace)
		if err != nil {
			return reject(fmt.Errorf("workspace: %w", err))
		}
		executeReq, err = GuardRequest(executeReq)
		if err != nil {
			return reject(fmt.Errorf("execution policy rejected assignment: %w", err))
		}
		logSink := NewRemoteLogSink(RemoteLogSinkConfig{
			Secrets: executeReq.Secrets,
			Post: func(stream string, seq int, data []byte) error {
				_, err := call(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/assignments/log", map[string]any{
					"assignmentId":     assignment.AssignmentID,
					"runId":            assignment.RunID,
					"activationId":     assignment.ActivationID,
					"attemptId":        assignment.AttemptID,
					"coordinatorEpoch": assignment.CoordinatorEpoch,
					"generation":       assignment.Generation,
					"stream":           stream,
					"seq":              seq,
					"data":             data,
				}, nil)
				return err
			},
		})
		executeReq.LogSink = logSink
		executeReq.AccountingSink = &RemoteAccountingSink{AppendSnapshot: func(snapshot contract.Accounting) error {
			_, err := callWithTimeout(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/assignments/accounting", map[string]any{"assignmentId": assignment.AssignmentID, "runId": assignment.RunID, "activationId": assignment.ActivationID, "attemptId": assignment.AttemptID, "coordinatorEpoch": assignment.CoordinatorEpoch, "generation": assignment.Generation, "accounting": snapshot}, nil, 2*time.Second)
			return err
		}}
		execCtx, execCancel := context.WithCancel(ctx)
		if executeReq.ExecutionDeadline != nil {
			deadlineCtx, deadlineCancel := context.WithDeadline(execCtx, *executeReq.ExecutionDeadline)
			parentCancel := execCancel
			execCtx, execCancel = deadlineCtx, func() { deadlineCancel(); parentCancel() }
		}
		if executeReq.Deadline != nil {
			deadlineCtx, deadlineCancel := context.WithDeadline(execCtx, *executeReq.Deadline)
			parentCancel := execCancel
			execCtx, execCancel = deadlineCtx, func() { deadlineCancel(); parentCancel() }
		}
		if executeReq.TimeoutSeconds != nil && *executeReq.TimeoutSeconds > 0 {
			timeoutCtx, timeoutCancel := context.WithTimeout(execCtx, time.Duration(*executeReq.TimeoutSeconds*float64(time.Second)))
			parentCancel := execCancel
			execCtx, execCancel = timeoutCtx, func() { timeoutCancel(); parentCancel() }
		}

		execCtx, endAttempt := telemetry.Start(telemetry.Extract(execCtx, executeReq.TraceContext), "muxlio.worker.attempt", telemetry.WorkerAttemptAttributes(executeReq.RunID, executeReq.ActivationID, executeReq.AttemptID, executeReq.Executor, cfg.WorkerID)...)
		executeReq.Context = execCtx
		inner := executor
		if cfg.ExecutorFactory != nil {
			inner = cfg.ExecutorFactory()
		} else if factory, ok := executor.(interface{ NewAttempt() Executor }); ok {
			inner = factory.NewAttempt()
		}
		if inner == nil {
			endAttempt(true)
			execCancel()
			return reject(fmt.Errorf("attempt executor is required"))
		}
		runExecutor := ArtifactTransferExecutor{
			Inner:    inner,
			Client:   artifacts.Client{BaseURL: base.String(), Token: cfg.Token, HTTP: client},
			WorkBase: cfg.WorkBase,
			Snapshot: func(seq int, snapshot state.ArtifactSnapshot) error {
				_, err := call(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/assignments/log", map[string]any{
					"assignmentId": assignment.AssignmentID, "runId": assignment.RunID,
					"activationId": assignment.ActivationID, "attemptId": assignment.AttemptID,
					"coordinatorEpoch": assignment.CoordinatorEpoch, "generation": assignment.Generation,
					"stream": "snapshot", "seq": seq, "snapshot": snapshot,
				}, nil)
				return err
			},
		}

		leaseUntil, _ := time.Parse(time.RFC3339Nano, assignment.LeaseUntil)
		active[assignment.AssignmentID] = &attempt{inputContext: execCtx, assignment: assignment, cancel: execCancel, leaseUntil: leaseUntil, leaseRenewedAt: time.Now()}
		children.Add(1)
		go func() {
			defer children.Done()
			defer execCancel()
			started := time.Now()
			r, e := runExecutor.Execute(executeReq)
			endAttempt(e != nil || r.Error != "" || execCtx.Err() != nil)
			telemetry.Duration(execCtx, "muxlio.execution.duration", time.Since(started), executeReq.Executor)
			if execCtx.Err() != nil {
				e = execCtx.Err()
				r.Outputs = nil
			}
			if r.ExecutionDurationMS == nil {
				elapsed := time.Since(started).Milliseconds()
				r.ExecutionDurationMS = &elapsed
			}
			_ = logSink.Close()
			select {
			case done <- execution{assignment.AssignmentID, r, e, executeReq.Secrets}:
			case <-ctx.Done():
			}
		}()
		return nil
	}
	type heartbeatResult struct {
		inputReceiptCount int
		leases            []state.AssignmentLease
		sentAt            time.Time
		draining          bool
		response          remoteHeartbeatResult
		status            int
		err               error
	}
	heartbeats := make(chan heartbeatResult, 1)
	heartbeating := false
	drainReported := false
	heartbeat := func() {
		if heartbeating {
			return
		}
		heartbeating = true
		updateLimit()
		telemetry.Slots(ctx, cfg.WorkerID, len(active), limit)
		for _, child := range active {
			telemetry.LeaseAge(ctx, cfg.WorkerID, time.Since(child.leaseRenewedAt))
		}
		ids := make([]string, 0, len(active))
		leases := make([]state.AssignmentLease, 0, len(active))
		for id, child := range active {
			ids = append(ids, id)
			leases = append(leases, state.AssignmentLease{AssignmentID: id, Generation: child.assignment.Generation, CoordinatorEpoch: child.assignment.CoordinatorEpoch})
		}
		slots := limit - len(active)
		if slots < 0 {
			slots = 0
		}
		// Capture the snapshot before starting I/O; only the supervisor owns active.
		capacity := limit
		receiptSnapshot := append([]remoteInputReceipt(nil), pendingInputReceipts...)
		body := remoteHeartbeat{InputReceipts: receiptSnapshot, InstanceID: registration.InstanceID, ProtocolVersion: remoteProtocolVersion, Active: ids, Leases: leases, Capacity: &capacity, AvailableSlots: slots, Draining: draining}
		sentAt := time.Now()
		children.Add(1)
		go func() {
			defer children.Done()
			var hb remoteHeartbeatResult
			status, err := call(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/heartbeat", body, &hb)
			if status == 409 {
				var result remoteRegistrationResult
				_, _ = call(http.MethodPost, "/v1/workers/register", registration, &result)
			}
			select {
			case heartbeats <- heartbeatResult{inputReceiptCount: len(receiptSnapshot), response: hb, status: status, err: err, leases: leases, sentAt: sentAt, draining: body.Draining}:
			case <-ctx.Done():
			}
		}()
	}
	heartbeat()
	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()
	pollTimer := time.NewTimer(0)
	defer pollTimer.Stop()
	pollReady := true
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !draining && cfg.ShutdownGracePeriod > 0 && parent.Err() != nil {
			shutdown = nil
			draining = true
			shutdownTimer = time.NewTimer(cfg.ShutdownGracePeriod)
			shutdownDeadline = shutdownTimer.C
			heartbeat()
		}
		updateLimit()
		if draining && drainReported && len(active) == 0 && len(inputBusy) == 0 && len(pendingInputReceipts) == 0 && !polling && !heartbeating {
			return nil
		}
		if pollReady && !polling && len(active) < limit {
			polling = true
			pollReady = false
			children.Add(1)
			go func() {
				defer children.Done()
				var assignment remoteAssignment
				status, err := retryableCall(http.MethodGet, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/assignments?instanceId="+url.QueryEscape(registration.InstanceID), nil, &assignment)
				select {
				case polled <- pollResult{assignment, status, err}:
				case <-ctx.Done():
				}
			}()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-shutdown:
			if cfg.ShutdownGracePeriod <= 0 {
				return nil
			}
			shutdown = nil
			draining = true
			shutdownTimer = time.NewTimer(cfg.ShutdownGracePeriod)
			shutdownDeadline = shutdownTimer.C
			heartbeat()
		case <-shutdownDeadline:
			return nil
		case <-drain:
			drain = nil
			draining = true
			heartbeat()
		case delivered := <-inputResults:
			delete(inputBusy, delivered.id)
			if len(delivered.receipt.AcknowledgedInputs) > 0 {
				pendingInputReceipts = append(pendingInputReceipts, delivered.receipt)
			}
		case h := <-heartbeats:
			heartbeating = false
			if h.err != nil {
				if h.status == 401 || h.status == 403 {
					return h.err
				}
				continue
			}
			pendingInputReceipts = pendingInputReceipts[h.inputReceiptCount:]
			for _, raw := range h.response.Commands {
				var command struct {
					SessionInput *struct {
						AssignmentID string                  `json:"assignmentId"`
						Inputs       []contract.SessionInput `json:"inputs"`
					} `json:"sessionInput"`
				}
				if json.Unmarshal(raw, &command) != nil || command.SessionInput == nil {
					continue
				}
				id := command.SessionInput.AssignmentID
				child := active[id]
				if child == nil || child.revoked || inputBusy[id] || child.inputContext == nil || remoteHeartbeatRevokesAssignment(id, child.assignment.CoordinatorEpoch, h.response) {
					continue
				}
				var req ExecuteRequest
				if contract.DecodeJSON(child.assignment.Request, &req) != nil || req.Session == nil || req.Session.Mode != "resume" {
					continue
				}
				req.Action, req.SessionInputs, req.Context = "steer", command.SessionInput.Inputs, child.inputContext
				req.Task, req.Outputs = "", nil
				req.RunID, req.ActivationID, req.AttemptID = child.assignment.RunID, child.assignment.ActivationID, child.assignment.AttemptID
				req.resourcesAdmitted = true
				inner := executor
				if cfg.ExecutorFactory != nil {
					inner = cfg.ExecutorFactory()
				} else if factory, ok := executor.(interface{ NewAttempt() Executor }); ok {
					inner = factory.NewAttempt()
				}
				if inner == nil {
					continue
				}
				inputBusy[id] = true
				assignment := child.assignment
				children.Add(1)
				go func() {
					defer children.Done()
					receipt := remoteInputReceipt{AssignmentID: assignment.AssignmentID, Generation: assignment.Generation, CoordinatorEpoch: assignment.CoordinatorEpoch}
					result, err := inner.Execute(req)
					if err == nil {
						// Receipts must name only input offered in this delivery.
						allowed := map[string]bool{}
						for _, input := range req.SessionInputs {
							allowed[input.RequestID] = true
						}
						for _, ack := range result.AcknowledgedInputs {
							if allowed[ack] {
								receipt.AcknowledgedInputs = append(receipt.AcknowledgedInputs, ack)
							}
						}
					}
					select {
					case inputResults <- inputResult{id, receipt}:
					case <-ctx.Done():
					}
				}()
			}
			if h.draining {
				drainReported = true
			}
			for _, lease := range h.leases {
				child := active[lease.AssignmentID]
				if child == nil || child.assignment.Generation != lease.Generation {
					continue
				}
				if remoteHeartbeatRevokesAssignment(child.assignment.AssignmentID, child.assignment.CoordinatorEpoch, h.response) {
					child.revoked = true
					child.cancel()
				} else if h.response.LeaseSeconds > 0 && !child.revoked {
					child.leaseRenewedAt = h.sentAt
					child.leaseUntil = h.sentAt.Add(time.Duration(h.response.LeaseSeconds) * time.Second)
				}
			}
			if draining && !drainReported {
				heartbeat()
			}
		case <-ticker.C:
			for _, child := range active {
				if !child.leaseUntil.IsZero() && !child.leaseUntil.After(time.Now()) {
					child.revoked = true
					child.cancel()
				}
			}
			heartbeat()
		case <-pollTimer.C:
			pollReady = true
		case p := <-polled:
			polling = false
			if ctx.Err() != nil {
				return nil
			}
			if p.err != nil {
				return p.err
			}
			if p.status == http.StatusNoContent {
				pollTimer.Reset(cfg.PollInterval)
			} else {
				if _, exists := active[p.assignment.AssignmentID]; exists {
					return fmt.Errorf("duplicate active assignment %q", p.assignment.AssignmentID)
				}
				if err := launch(p.assignment); err != nil {
					return err
				}
				pollReady = true
			}
		case finished := <-done:
			child := active[finished.id]
			assignment := child.assignment
			executeReq := ExecuteRequest{Secrets: finished.secrets}
			result, execErr := finished.result, finished.err
			result.Logs = boundedLog(RedactSecrets(result.Logs, executeReq.Secrets))
			if result.Reconciliation != nil {
				reconciliation := *result.Reconciliation
				reconciliation.Reason = diagnostic.Redact(RedactSecrets(reconciliation.Reason, executeReq.Secrets))
				result.Reconciliation = &reconciliation
			}
			completion := remoteCompletion{OutputErrors: result.OutputErrors, AcknowledgedInputs: result.AcknowledgedInputs, AssignmentID: assignment.AssignmentID, RunID: assignment.RunID, ActivationID: assignment.ActivationID, AttemptID: assignment.AttemptID, CoordinatorEpoch: assignment.CoordinatorEpoch, Generation: assignment.Generation, State: "succeeded", Outputs: result.Outputs, Logs: result.Logs, Accounting: result.Accounting, RemoteAccounting: result.RemoteAccounting, Session: result.Session, SessionID: result.SessionID, Reconciliation: result.Reconciliation, RemoteTask: result.RemoteTask, ExecutionDurationMS: result.ExecutionDurationMS}
			if child.revoked {
				completion.State = "cancelled"
				completion.Outputs = nil
				completion.Error = &remoteError{Code: "assignment_revoked", Message: "executor returned after assignment revocation; external effects may have completed", Retryable: false}
			} else if execErr != nil || result.Error != "" {
				completion.State = "failed"
				message := result.Error
				if execErr != nil {
					message = execErr.Error()
				}
				completion.Error = &remoteError{Code: "execution_failed", Message: diagnostic.Redact(RedactSecrets(message, executeReq.Secrets)), Retryable: true}
				var d *diagnostic.Diagnostic
				if errors.As(execErr, &d) {
					var safe diagnostic.Diagnostic
					if contract.DecodeJSON(diagnostic.WithSecrets(d, executeReq.Secrets), &safe) == nil {
						completion.Error.Diagnostic = &safe
					}
				}
			}

			children.Add(1)
			go func() {
				defer children.Done()
				status, err := retryableCall(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/assignments/complete", completion, nil)
				select {
				case receipts <- receipt{completion.AssignmentID, status, err}:
				case <-ctx.Done():
				}
			}()
		case r := <-receipts:
			delete(active, r.id)
			if ctx.Err() != nil {
				return nil
			}
			if r.err != nil && r.status != 409 {
				return r.err
			}
			pollReady = true
		}
	}
}
