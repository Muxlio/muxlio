package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
)

const remoteProtocolVersion = "v1"

type RemoteCapability struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}
type remoteRegistration struct {
	ProtocolVersion string             `json:"protocolVersion"`
	WorkerID        string             `json:"workerId"`
	OS              string             `json:"os,omitempty"`
	Architecture    string             `json:"architecture,omitempty"`
	Capacity        int                `json:"capacity"`
	Capabilities    []RemoteCapability `json:"capabilities"`
	Labels          map[string]string  `json:"labels,omitempty"`
}
type remoteRegistrationResult struct {
	CoordinatorEpoch int64 `json:"coordinatorEpoch"`
	HeartbeatSeconds int   `json:"heartbeatSeconds"`
}
type remoteHeartbeat struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Active          []string `json:"activeAssignments,omitempty"`
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
	Request          json.RawMessage `json:"request"`
}
type remoteError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}
type remoteCompletion struct {
	AssignmentID     string            `json:"assignmentId"`
	RunID            string            `json:"runId"`
	ActivationID     string            `json:"activationId"`
	AttemptID        string            `json:"attemptId"`
	CoordinatorEpoch int64             `json:"coordinatorEpoch"`
	Generation       int64             `json:"generation"`
	State            string            `json:"state"`
	Outputs          map[string]any    `json:"outputs,omitempty"`
	Error            *remoteError      `json:"error,omitempty"`
	Logs             string            `json:"logs,omitempty"`
	Usage            *state.TokenUsage `json:"usage,omitempty"`
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
}

// RunRemoteWorker registers and polls until ctx is cancelled. Assignments are
// executed through the same Executor contract used by local workflows.
func RunRemoteWorker(ctx context.Context, cfg RemoteWorkerConfig, executor Executor) error {
	if cfg.WorkerID == "" || cfg.CoordinatorURL == "" || executor == nil {
		return fmt.Errorf("worker id, coordinator URL, and executor are required")
	}
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
	call := func(method, path string, body any, target any) (int, error) {
		var reader io.Reader
		if body != nil {
			raw, e := json.Marshal(body)
			if e != nil {
				return 0, e
			}
			reader = bytes.NewReader(raw)
		}

		// Create a timeout context for this specific request
		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	registration := remoteRegistration{ProtocolVersion: remoteProtocolVersion, WorkerID: cfg.WorkerID, OS: runtime.GOOS, Architecture: runtime.GOARCH, Capacity: cfg.Capacity, Capabilities: cfg.Capabilities, Labels: cfg.Labels}
	var registered remoteRegistrationResult

	active := make([]string, 0)
	// Helper function with retry logic for coordinator calls
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
				_, _ = call(http.MethodPost, "/v1/workers/register", registration, &registered)
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

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		var heartbeat remoteHeartbeatResult
		if _, err := retryableCall(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/heartbeat", remoteHeartbeat{ProtocolVersion: remoteProtocolVersion, Active: active}, &heartbeat); err != nil {
			return err
		}
		var assignment remoteAssignment
		status, err := retryableCall(http.MethodGet, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/assignments", nil, &assignment)
		if err != nil {
			return err
		}
		if status == http.StatusNoContent {
			t := time.NewTimer(cfg.PollInterval)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil
			case <-t.C:
			}
			continue
		}
		var executeReq ExecuteRequest
		if err := json.Unmarshal(assignment.Request, &executeReq); err != nil {
			return fmt.Errorf("invalid assignment request: %w", err)
		}
		executeReq, err = ApplyWorkerWorkspace(executeReq, cfg.Workspace)
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		executeReq, err = GuardRequest(executeReq)
		if err != nil {
			return fmt.Errorf("execution policy rejected assignment: %w", err)
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
		execCtx, execCancel := context.WithCancel(ctx)
		defer execCancel()
		executeReq.Context = execCtx
		runExecutor := ArtifactTransferExecutor{
			Inner:    executor,
			Client:   artifacts.Client{BaseURL: base.String(), Token: cfg.Token, HTTP: client},
			WorkBase: cfg.WorkBase,
		}
		active = []string{assignment.AssignmentID}
		type execution struct {
			result ExecuteResult
			err    error
		}
		done := make(chan execution, 1)
		go func() {
			r, e := runExecutor.Execute(executeReq)
			_ = logSink.Close()
			done <- execution{r, e}
		}()
		ticker := time.NewTicker(cfg.HeartbeatInterval)
		var finished execution
		revoked := false
		applyHeartbeat := func(hb remoteHeartbeatResult) {
			if remoteHeartbeatRevokesAssignment(assignment.AssignmentID, assignment.CoordinatorEpoch, hb) {
				revoked = true
				execCancel()
			}
		}
	executing:
		for {
			select {
			case finished = <-done:
				break executing
			case <-ctx.Done():
				ticker.Stop()
				// Wait for the executor goroutine so context-aware adapters can
				// kill and reap their subprocesses before shutdown returns.
				<-done
				return nil
			case <-ticker.C:
				var hb remoteHeartbeatResult
				status, err := call(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/heartbeat", remoteHeartbeat{ProtocolVersion: remoteProtocolVersion, Active: active}, &hb)
				if err != nil && status == 409 {
					_, _ = call(http.MethodPost, "/v1/workers/register", registration, &registered)
				}
				if err != nil && (status == 401 || status == 403) {
					ticker.Stop()
					return err
				}
				if err == nil {
					applyHeartbeat(hb)
					if revoked {
						finished = <-done
						break executing
					}
				}
			}
		}
		ticker.Stop()
		result, execErr := finished.result, finished.err
		result.Logs = boundedLog(RedactSecrets(result.Logs, executeReq.Secrets))
		completion := remoteCompletion{AssignmentID: assignment.AssignmentID, RunID: assignment.RunID, ActivationID: assignment.ActivationID, AttemptID: assignment.AttemptID, CoordinatorEpoch: assignment.CoordinatorEpoch, Generation: assignment.Generation, State: "succeeded", Outputs: result.Outputs, Logs: result.Logs, Usage: result.Usage}
		if revoked {
			completion.State = "cancelled"
			completion.Outputs = nil
			completion.Error = &remoteError{Code: "assignment_revoked", Message: "assignment revoked by coordinator", Retryable: false}
		} else if execErr != nil || result.Error != "" {
			completion.State = "failed"
			message := result.Error
			if execErr != nil {
				message = execErr.Error()
			}
			completion.Error = &remoteError{Code: "execution_failed", Message: RedactSecrets(message, executeReq.Secrets), Retryable: true}
		}
		if status, err := retryableCall(http.MethodPost, "/v1/workers/"+url.PathEscape(cfg.WorkerID)+"/assignments/complete", completion, nil); err != nil && status != 409 {
			// Clear active assignment before returning on completion error
			active = active[:0]
			return err
		}
		active = active[:0]
	}
}
