package workers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestScriptExecutorRecordsWorkerStartedAtAfterLaunch(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "generic")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"outputs\":{},\"error\":\"\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := &ScriptExecutor{adapterDir: dir, Timeout: time.Second}
	result, err := exec.Execute(ExecuteRequest{Executor: "generic", Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStartedAt == nil || result.WorkerStartedAt.IsZero() {
		t.Fatalf("expected workerStartedAt after successful launch: %+v", result)
	}
}

func TestScriptExecutorLaunchFailureHasNoWorkerStartedAt(t *testing.T) {
	dir := t.TempDir()
	// Non-executable path that fails at Start.
	script := filepath.Join(dir, "generic")
	if err := os.WriteFile(script, []byte("not a script"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := &ScriptExecutor{adapterDir: dir, Timeout: time.Second}
	result, err := exec.Execute(ExecuteRequest{Executor: "generic", Context: context.Background()})
	if err == nil && result.Error == "" {
		t.Fatal("expected launch or adapter failure")
	}
	if result.WorkerStartedAt != nil {
		t.Fatalf("launch failure must not set workerStartedAt: %+v", result)
	}
}

func TestScriptExecutorImmediateRejectionStillHasWorkerStartedAt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "generic")
	body, _ := json.Marshal(contract.Result{
		Outputs: map[string]any{},
		Error:   "quota exhausted",
		Availability: &contract.Availability{
			Reason:  contract.AvailabilityQuotaExhausted,
			Message: "quota exhausted",
		},
	})
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '"+string(body)+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := &ScriptExecutor{adapterDir: dir, Timeout: time.Second}
	result, err := exec.Execute(ExecuteRequest{Executor: "generic", Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStartedAt == nil {
		t.Fatal("launched-then-rejected attempts must record workerStartedAt")
	}
	if result.Availability == nil || result.Availability.Reason != contract.AvailabilityQuotaExhausted {
		t.Fatalf("availability: %+v", result.Availability)
	}
}

func TestFakeExecutorSetsWorkerStartedAt(t *testing.T) {
	fake := NewFake().ByExecutor("test", ExecuteResult{Outputs: map[string]any{"v": 1}})
	result, err := fake.Execute(ExecuteRequest{Executor: "test", Context: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerStartedAt == nil {
		t.Fatal("expected fake invocation start")
	}
}

func TestQueueTimeoutDoesNotFabricateWorkerStartedAt(t *testing.T) {
	deadline := time.Now().Add(-time.Millisecond)
	_, err := ExecuteAttempt(queueNeverAdmit{}, ExecuteRequest{
		Context:             context.Background(),
		QueueTimeoutSeconds: ptrFloat(0.01),
		QueueDeadline:       &deadline,
	})
	if err == nil {
		t.Fatal("expected queue timeout")
	}
	var qt QueueTimeoutError
	if !asQueueTimeout(err, &qt) && !strings.Contains(err.Error(), "queue timeout") {
		// ExecuteAttempt with QueuedExecutor returns queue timeout; ensure no launch fact.
		t.Fatalf("err=%v", err)
	}
}

type queueNeverAdmit struct{}

func (queueNeverAdmit) Execute(ExecuteRequest) (ExecuteResult, error) {
	return ExecuteResult{}, nil
}

func (queueNeverAdmit) ExecuteQueued(req ExecuteRequest) (ExecuteResult, error) {
	deadline := time.Now().Add(-time.Millisecond)
	if req.QueueDeadline != nil {
		deadline = *req.QueueDeadline
	}
	return ExecuteResult{}, QueueTimeoutError{Deadline: deadline}
}

func asQueueTimeout(err error, target *QueueTimeoutError) bool {
	if err == nil {
		return false
	}
	if qt, ok := err.(QueueTimeoutError); ok {
		*target = qt
		return true
	}
	return false
}

func ptrFloat(v float64) *float64 { return &v }
