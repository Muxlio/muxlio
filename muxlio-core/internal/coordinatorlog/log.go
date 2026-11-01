// Package coordinatorlog writes structured coordinator lifecycle lines to the
// service process stdout (captured as logs/service.log by muxlio service).
package coordinatorlog

import (
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/muxlio/muxlio-core/internal/workers"
)

var (
	mu      sync.Mutex
	out     io.Writer = os.Stdout
	secrets           = map[string]string{}
)

var bearerToken = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-+/=]+`)

// SetOutput redirects coordinator log lines (tests).
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if w == nil {
		out = io.Discard
		return
	}
	out = w
}

// RegisterSecrets adds secret values that must never appear in log output.
func RegisterSecrets(m map[string]string) {
	if len(m) == 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for k, v := range m {
		if v != "" {
			secrets[k] = v
		}
	}
}

func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	out = os.Stdout
	secrets = map[string]string{}
}

func redactString(s string) string {
	s = bearerToken.ReplaceAllString(s, "Bearer [REDACTED]")
	if len(secrets) > 0 {
		s = workers.RedactSecrets(s, secrets)
	}
	return s
}

func emit(event string, fields map[string]any) {
	line := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": event,
	}
	for k, v := range fields {
		switch val := v.(type) {
		case string:
			line[k] = redactString(val)
		case int, int64, float64, bool:
			line[k] = val
			// Lifecycle records intentionally accept only scalar metadata. Never
			// serialize arbitrary structs, maps, or prompt/result objects here.
		}
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	_, _ = io.WriteString(out, string(raw)+"\n")
}

// RunAccepted logs that a run was submitted and scheduled.
func RunAccepted(runID, workflow string) {
	emit("run.accepted", map[string]any{"runId": runID, "workflow": workflow})
}

// RunStateChanged logs a durable run state transition.
func RunStateChanged(runID, from, to string) {
	emit("run.state_changed", map[string]any{"runId": runID, "from": from, "to": to})
}

// RunRetry logs that a failed or cancelled run is being retried.
func RunRetry(runID string) {
	emit("run.retry", map[string]any{"runId": runID})
}

// RunFailed logs terminal run failure with a redacted error summary.
func RunFailed(runID, message string) {
	msg := redactString(message)
	if len(msg) > 512 {
		msg = msg[:512] + "…"
	}
	emit("run.failed", map[string]any{"runId": runID, "error": msg})
}

// ActivationStarted logs that an activation began executing.
func ActivationStarted(runID, activationID string) {
	emit("activation.started", map[string]any{"runId": runID, "activationId": activationID})
}

// ActivationFinished logs activation terminal state.
func ActivationFinished(runID, activationID, state string) {
	emit("activation.finished", map[string]any{"runId": runID, "activationId": activationID, "state": state})
}

// AttemptStarted logs the beginning of an execution attempt.
func AttemptStarted(runID, activationID, executor string, attempt int) {
	emit("attempt.started", map[string]any{
		"runId": runID, "activationId": activationID, "executor": executor, "attempt": attempt,
	})
}

// AttemptFinished logs attempt completion.
func AttemptFinished(runID, activationID, executor string, attempt int, state, failure string) {
	fields := map[string]any{
		"runId": runID, "activationId": activationID, "executor": executor,
		"attempt": attempt, "state": state,
	}
	if failure != "" {
		msg := redactString(failure)
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		fields["failure"] = msg
	}
	emit("attempt.finished", fields)
}

// AttemptRetry logs a scheduled retry before the next attempt.
func AttemptRetry(runID, activationID, executor string, attempt int) {
	emit("attempt.retry", map[string]any{
		"runId": runID, "activationId": activationID, "executor": executor, "attempt": attempt,
	})
}

// WorkerAssigned logs dispatch of work to a worker.
func WorkerAssigned(runID, activationID, workerID, executor, attemptID string) {
	emit("worker.assigned", map[string]any{
		"runId": runID, "activationId": activationID, "workerId": workerID,
		"executor": executor, "attemptId": attemptID,
	})
}

// WorkerCompleted logs worker acknowledgement of an assignment.
func WorkerCompleted(runID, activationID, workerID, executor, attemptID, state string) {
	emit("worker.completed", map[string]any{
		"runId": runID, "activationId": activationID, "workerId": workerID,
		"executor": executor, "attemptId": attemptID, "state": state,
	})
}

// ParseAttemptNumber extracts the retry counter from a muxlio attempt id.
func ParseAttemptNumber(attemptID string) int {
	const suffix = "-attempt-"
	i := strings.LastIndex(attemptID, suffix)
	if i < 0 {
		return 0
	}
	n := 0
	for _, c := range attemptID[i+len(suffix):] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
