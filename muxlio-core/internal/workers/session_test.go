//go:build unix

package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

// This fixture speaks only the shared protocol. Provider state checks belong
// inside the adapter; workers must never reconstruct missing conversation state.
func TestSessionAdapterProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] != "muxlio-session-fixture" {
		return
	}
	var req contract.Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		os.Exit(2)
	}
	result := contract.Result{Outputs: map[string]any{}}
	defer func() { _ = json.NewEncoder(os.Stdout).Encode(result); os.Exit(0) }()
	if req.Session == nil {
		return
	}
	statePath := filepath.Join(os.Getenv("SESSION_FIXTURE_STATE"), "provider-state")
	if req.Session.Mode == "create" {
		result.Session = &contract.SessionHandle{ID: "opaque-provider-handle", Executor: req.Executor, RunnerVersion: "runner-1", ExpiresAt: time.Now().Add(time.Hour).UTC(), Recoverable: req.Task != "ephemeral"}
		if req.Task == "non-utc" {
			result.Session.ExpiresAt = result.Session.ExpiresAt.In(time.FixedZone("provider", 5*3600+45*60))
		}
		_ = os.WriteFile(statePath, []byte("runner-1"), 0600)
		return
	}
	runner, err := os.ReadFile(statePath)
	if err != nil {
		result.Error = "provider session state missing"
		return
	}
	if req.Session.Handle.RunnerVersion != string(runner) {
		result.Error = "runner version incompatible"
		return
	}

	if req.Task == "multiplex" {
		root := filepath.Join(os.Getenv("SESSION_FIXTURE_STATE"), "multiplex")
		if err := os.MkdirAll(filepath.Join(root, filepath.Base(req.WorkDir)), 0700); err != nil {
			result.Error = err.Error()
			return
		}
		ready := filepath.Join(root, "ready")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			entries, _ := os.ReadDir(root)
			if len(entries) >= 2 {
				_ = os.WriteFile(ready, nil, 0600)
			}
			if _, err := os.Stat(ready); err == nil {
				result.Session = req.Session.Handle
				return
			}
			time.Sleep(time.Millisecond)
		}
		result.Error = "multiplex-capable session was serialized locally"
		return
	}
	marker := filepath.Join(os.Getenv("SESSION_FIXTURE_STATE"), "active")
	if err := os.Mkdir(marker, 0700); err != nil {
		result.Error = "concurrent session corruption"
		return
	}
	defer os.Remove(marker)
	if req.Task == "block" {
		time.Sleep(time.Minute)
	}
	time.Sleep(40 * time.Millisecond)
	if req.Task == "fail" {
		result.Error = "turn failed"
		return
	}
	if req.Task == "omit" {
		return
	}
	result.Session = req.Session.Handle
	if req.Task == "restart" {
		copy := *req.Session.Handle
		copy.ID = "new-conversation"
		result.Session = &copy
	}
	result.Outputs["context"] = "resumed"
}

func sessionExecutor(t *testing.T) *ScriptExecutor {
	t.Helper()
	dir, workspace := t.TempDir(), t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := writeManifest(t, dir, "test", adapterManifest{Executor: "test", ExecutorVersion: "1", CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}, Capabilities: map[string]bool{contract.CapabilitySession: true}})
	body := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=TestSessionAdapterProcess -- muxlio-session-fixture\n", strings.ReplaceAll(binary, "'", "'\\''"))
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	s := NewScriptExecutor(dir, t.TempDir())
	s.Workspace = workspace
	s.WorkerID = "original-worker"
	s.WorkerEnv = map[string]string{"SESSION_FIXTURE_STATE": t.TempDir()}
	return s
}

func createSession(t *testing.T, s *ScriptExecutor, task string) *contract.SessionHandle {
	t.Helper()
	result, err := s.Execute(ExecuteRequest{Executor: "test", Task: task, Session: &contract.SessionRequest{Mode: "create"}})
	if err != nil || result.Error != "" || result.Session == nil {
		t.Fatalf("create: %+v %v", result, err)
	}
	return result.Session
}

func TestPersistentSessionResumeAndAffinity(t *testing.T) {
	s := sessionExecutor(t)
	oneShot, err := s.Execute(ExecuteRequest{Executor: "test"})
	if err != nil || oneShot.Error != "" || oneShot.Session != nil {
		t.Fatalf("one-shot: %+v %v", oneShot, err)
	}
	handle := createSession(t, s, "")
	if handle.OwnerWorker != s.WorkerID || handle.OwnerEnvironment != s.SessionEnvironment() {
		t.Fatalf("ownership: %+v", handle)
	}
	req := ExecuteRequest{Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}}
	// A new runner instance may recover the same persisted, recoverable state.
	restarted := NewScriptExecutor(s.adapterDir, s.workBase)
	restarted.WorkerEnv = s.WorkerEnv
	restarted.Workspace = s.Workspace
	restarted.WorkerID = s.WorkerID
	result, err := restarted.Execute(req)
	if err != nil || result.Error != "" || result.Outputs["context"] != "resumed" || *result.Session != *handle {
		t.Fatalf("resume: %+v %v", result, err)
	}
	restarted.WorkerID = "other-worker"
	result, err = restarted.Execute(req)
	if err != nil || !strings.Contains(result.Error, "owner") {
		t.Fatalf("affinity: %+v %v", result, err)
	}
	restarted.WorkerID = s.WorkerID
	restarted.WorkerEnv = map[string]string{"CONFIG": "changed"}
	result, err = restarted.Execute(req)
	if err != nil || !strings.Contains(result.Error, "environment") {
		t.Fatalf("environment: %+v %v", result, err)
	}
}

func TestNonmultiplexedSessionSerializesAcrossRunnerInstances(t *testing.T) {
	s := sessionExecutor(t)
	handle := createSession(t, s, "")
	other := NewScriptExecutor(s.adapterDir, s.workBase)
	other.WorkerEnv = s.WorkerEnv
	other.Workspace = s.Workspace
	other.WorkerID = s.WorkerID
	start := make(chan struct{})
	failures := make(chan string, 2)
	var wg sync.WaitGroup
	for _, runner := range []*ScriptExecutor{s, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := runner.NewAttempt().Execute(ExecuteRequest{Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
			if err != nil || r.Error != "" {
				failures <- fmt.Sprintf("%+v %v", r, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

func TestSessionExpirationSurvivesTimezoneRoundTrip(t *testing.T) {
	s := sessionExecutor(t)
	handle := createSession(t, s, "non-utc")
	result, err := s.Execute(ExecuteRequest{Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
	if err != nil || result.Error != "" || result.Session == nil {
		t.Fatalf("valid expiration timezone rejected on resume: %+v %v", result, err)
	}
}

func TestSessionMissingStateAndFalseContinuityFailClosed(t *testing.T) {
	for _, scenario := range []string{"registry", "provider", "provider-upgrade", "fail", "omit", "restart", "ephemeral", "runner", "expired", "adapter-version"} {
		t.Run(scenario, func(t *testing.T) {
			s := sessionExecutor(t)
			task := ""
			if scenario == "ephemeral" {
				task = "ephemeral"
			}
			handle := createSession(t, s, task)
			req := ExecuteRequest{Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}}
			switch scenario {
			case "registry":
				_ = os.Remove(s.sessionPath("test", handle.ID))
			case "provider":
				_ = os.Remove(filepath.Join(s.WorkerEnv["SESSION_FIXTURE_STATE"], "provider-state"))
			case "provider-upgrade":
				if err := os.WriteFile(filepath.Join(s.WorkerEnv["SESSION_FIXTURE_STATE"], "provider-state"), []byte("runner-2"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ephemeral":
				s.sessionInstance = newSessionInstance()
			case "runner":
				handle.RunnerVersion = "other-runner"
			case "expired":
				handle.ExpiresAt = time.Now().Add(-time.Minute)
			case "adapter-version":
				raw, _ := json.Marshal(adapterManifest{Executor: "test", ExecutorVersion: "2", CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}, Capabilities: map[string]bool{contract.CapabilitySession: true}})
				_ = os.WriteFile(s.scriptPath("test")+".json", raw, 0600)
			default:
				req.Task = scenario
			}
			r, err := s.Execute(req)
			if err == nil && r.Error == "" {
				t.Fatalf("accepted missing/incompatible continuity: %+v", r)
			}
			if r.Session != nil {
				t.Fatalf("failed resume exposed usable session: %+v", r)
			}
			if scenario == "fail" || scenario == "omit" || scenario == "restart" {
				req.Task = ""
				r, err = s.Execute(req)
				if err == nil && !strings.Contains(r.Error, "state missing") {
					t.Fatalf("failed turn resumed: %+v %v", r, err)
				}
			}
		})
	}
}

func TestSessionLockCancellationAndExplicitMultiplexing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	release, err := lockSession(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	release2, err := lockSession(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	release2()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if unlock, err := lockSession(ctx, path, false); err == nil {
		unlock()
		t.Fatal("exclusive lock bypassed active shared lock")
	}
}

func TestInterruptedSessionTurnCannotResume(t *testing.T) {
	s := sessionExecutor(t)
	handle := createSession(t, s, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan ExecuteResult, 1)
	go func() {
		result, err := s.NewAttempt().Execute(ExecuteRequest{Context: ctx, Executor: "test", Task: "block", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
		if err != nil {
			result.Error = err.Error()
		}
		done <- result
	}()
	// Wait until the adapter has entered the mutable turn before cancelling.
	marker := filepath.Join(s.WorkerEnv["SESSION_FIXTURE_STATE"], "active")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("adapter did not start the session turn")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case result := <-done:
		if result.Error == "" || result.Session != nil {
			t.Fatalf("cancelled turn exposed continuity: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session turn did not stop after cancellation")
	}
	result, err := s.Execute(ExecuteRequest{Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
	if err != nil || !strings.Contains(result.Error, "state missing") || result.Session != nil {
		t.Fatalf("interrupted turn resumed: %+v %v", result, err)
	}
}

func TestSessionDispatchMatchesOnlyCompatibleOwner(t *testing.T) {
	s := sessionExecutor(t)
	h := createSession(t, s, "")
	req := ExecuteRequest{Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: h}}
	raw, _ := json.Marshal(req)
	manifest, _ := s.Descriptor("test")
	caps := []state.WorkerCapability{{Name: "test", Version: "1", Contract: manifest}}
	labels := map[string]string{SessionEnvironmentLabel: s.SessionEnvironment()}
	if !MatchWorkerDispatch(s.WorkerID, "test", raw, caps, labels) {
		t.Fatal("owner did not match")
	}
	if MatchWorkerDispatch("other", "test", raw, caps, labels) || MatchWorkerDispatch(s.WorkerID, "test", raw, caps, nil) {
		t.Fatal("nonportable session migrated")
	}
	manifest.Capabilities[contract.CapabilitySession] = false
	if MatchWorkerDispatch(s.WorkerID, "test", raw, caps, labels) {
		t.Fatal("incompatible adapter matched")
	}
}

func TestSessionAdapterMayExplicitlyMultiplex(t *testing.T) {
	s := sessionExecutor(t)
	manifest, err := s.Descriptor("test")
	if err != nil {
		t.Fatal(err)
	}
	manifest.Capabilities[contract.CapabilitySessionMultiplex] = true
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(s.scriptPath("test")+".json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	handle := createSession(t, s, "")
	results := make(chan ExecuteResult, 2)
	for range 2 {
		go func() {
			r, err := s.NewAttempt().Execute(ExecuteRequest{Executor: "test", Task: "multiplex", Session: &contract.SessionRequest{Mode: "resume", Handle: handle}})
			if err != nil {
				r.Error = err.Error()
			}
			results <- r
		}()
	}
	for range 2 {
		r := <-results
		if r.Error != "" || r.Session == nil {
			t.Fatalf("multiplex: %+v", r)
		}
	}
}
