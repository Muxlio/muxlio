package recovery_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// Faults live at actual process, persistence, and HTTP boundaries. The script
// executor writes a ledger as well as an artifact, so replay cannot hide behind
// an identical final result. No production fault switches are needed.
type lifecycleProcessConfig struct {
	Kind, DB, Blobs, Ready, Boundary, URL, Adapters, Work string
	Plan                                                  *plan.Plan
}

type lifecycleReady struct{ URL, RunID string }

type lifecycleExecutor func(workers.ExecuteRequest) (workers.ExecuteResult, error)

func (f lifecycleExecutor) Execute(r workers.ExecuteRequest) (workers.ExecuteResult, error) {
	return f(r)
}

func lifecycleWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

// Invoked only in an isolated copy of the test binary. SIGKILL bypasses context
// cancellation, store.Close, and every deferred cleanup in the crashed process.
func TestLifecycleFaultProcess(t *testing.T) {
	path := os.Getenv("MUXLIO_LIFECYCLE_FAULT_PROCESS")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg lifecycleProcessConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Kind == "worker" {
		inner := workers.NewScriptExecutor(cfg.Adapters, cfg.Work)
		gate := lifecycleExecutor(func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
			r, err := inner.Execute(req)
			if err != nil || r.Error != "" {
				return r, err
			}
			lifecycleWrite(t, cfg.Boundary, true)
			<-req.Context.Done() // killed after external effect, before receipt
			return r, req.Context.Err()
		})
		if err := workers.RunRemoteWorker(context.Background(), lifecycleWorkerConfig(cfg.URL, cfg.Work), gate); err != nil {
			t.Fatal(err)
		}
		return
	}
	store := lifecycleOpen(t, cfg.DB)
	coordinator := api.NewCoordinator(store)
	blobs, err := artifacts.NewLocal(cfg.Blobs)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.Artifacts = blobs
	server := httptest.NewServer(api.NewRouterWithArtifacts(store, nil, blobs, api.NewWorkerProtocolHandler(coordinator)))
	var executor workers.Executor = coordinator
	if cfg.Boundary != "" {
		executor = lifecycleExecutor(func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
			r, err := coordinator.Execute(req)
			if err == nil {
				lifecycleWrite(t, cfg.Boundary, true)
				select {} // worker result committed; activation not yet checkpointed
			}
			return r, err
		})
	}
	engine := plan.NewEngineWithStore(cfg.Plan, executor, store)
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, cfg.Ready, lifecycleReady{server.URL, id})
	if err := engine.Run(id); err != nil {
		t.Fatal(err)
	}
	select {} // stay alive until parent kills us
}

func lifecycleStartProcess(t *testing.T, cfg lifecycleProcessConfig) func() {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "process.json")
	lifecycleWrite(t, path, cfg)
	log, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleFaultProcess$", "-test.timeout=45s")
	cmd.Env = append(os.Environ(), "MUXLIO_LIFECYCLE_FAULT_PROCESS="+path)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	stopped := false
	kill := func() {
		if stopped {
			return
		}
		stopped = true
		killErr := cmd.Process.Kill()
		waitErr := cmd.Wait()
		log.Close()
		if killErr != nil || waitErr == nil {
			t.Errorf("expected killed process: kill=%v wait=%v", killErr, waitErr)
		}
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Logf("child process: %s", data)
		}
	}
	t.Cleanup(kill)
	return kill
}

func lifecycleWait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func lifecycleOpen(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func lifecyclePlan() *plan.Plan {
	return &plan.Plan{IRVersion: "1.0", Name: "lifecycle-fault", Root: "root", Subgraphs: map[string]plan.Subgraph{
		"root": {Nodes: []plan.Node{{ID: "work", Kind: plan.KindExecute, Executor: "test/lifecycle", Outputs: map[string]plan.OutputDecl{"file": {Path: "result.txt", Mime: "text/plain"}}}}},
	}}
}

func lifecycleScript(t *testing.T) (string, string) {
	t.Helper()
	dir, ledger := t.TempDir(), filepath.Join(t.TempDir(), "effects")
	// Quote the path as shell data, including on machines with apostrophes in TMPDIR.
	quoted := "'" + strings.ReplaceAll(ledger, "'", "'\"'\"'") + "'"
	body := "#!/bin/sh\nset -eu\nprintf 'effect\\n' >> " + quoted + "\nprintf 'committed artifact' > result.txt\nprintf '{}'\n"
	if err := os.WriteFile(filepath.Join(dir, "test-lifecycle"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test-lifecycle.json"), []byte(`{"manifestVersion":"1","executor":"test/lifecycle","capabilities":{"artifacts":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, ledger
}

func lifecycleWorkerConfig(url, work string) workers.RemoteWorkerConfig {
	return workers.RemoteWorkerConfig{CoordinatorURL: url, WorkerID: "fault-worker", Capacity: 2,
		Capabilities: []workers.RemoteCapability{lifecycleCapability()}, WorkBase: work,
		PollInterval: 5 * time.Millisecond, HeartbeatInterval: 50 * time.Millisecond}
}

func lifecycleCapability() workers.RemoteCapability {
	return workers.RemoteCapability{Name: "test/lifecycle", Contract: &contract.AdapterManifest{
		ManifestVersion: "1", Executor: "test/lifecycle", Capabilities: map[string]bool{contract.CapabilityArtifacts: true},
	}}
}

func lifecycleWorker(t *testing.T, cfg workers.RemoteWorkerConfig, executor workers.Executor) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	done := make(chan error, 1)
	go func() { done <- workers.RunRemoteWorker(ctx, cfg, executor) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("worker: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func lifecycleEffects(t *testing.T, ledger string, want int) {
	t.Helper()
	raw, err := os.ReadFile(ledger)
	if err != nil || strings.Count(string(raw), "effect\n") != want {
		t.Fatalf("external effects=%q error=%v; want %d", raw, err, want)
	}
}

func lifecycleArtifact(t *testing.T, blobs *artifacts.Local, outputs map[string]any) {
	t.Helper()
	ref, ok := outputs["file"].(map[string]any)
	if !ok {
		t.Fatalf("missing artifact reference: %#v", outputs)
	}
	digest, _ := ref["digest"].(string)
	blob, err := blobs.Get(digest)
	if err != nil || string(blob.Data) != "committed artifact" {
		t.Fatalf("artifact lost: %v", err)
	}
}

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (f lifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLifecycleCoordinatorKilledAfterResultCommit(t *testing.T) {
	dir := t.TempDir()
	cfg := lifecycleProcessConfig{Kind: "coordinator", DB: filepath.Join(dir, "state.db"), Blobs: filepath.Join(dir, "blobs"), Ready: filepath.Join(dir, "ready"), Boundary: filepath.Join(dir, "committed"), Plan: lifecyclePlan()}
	kill := lifecycleStartProcess(t, cfg)
	var ready lifecycleReady
	lifecycleWait(t, "coordinator startup", func() bool {
		raw, _ := os.ReadFile(cfg.Ready)
		return json.Unmarshal(raw, &ready) == nil
	})
	adapters, ledger := lifecycleScript(t)
	stop := lifecycleWorker(t, lifecycleWorkerConfig(ready.URL, t.TempDir()), workers.NewScriptExecutor(adapters, t.TempDir()))
	lifecycleWait(t, "durable result boundary", func() bool { _, err := os.Stat(cfg.Boundary); return err == nil })
	stop()
	kill()
	store := lifecycleOpen(t, cfg.DB)
	acts, err := store.ListActivations(ready.RunID)
	if err != nil || len(acts) != 1 || acts[0].State == plan.ActSucceeded {
		t.Fatalf("crash missed transition boundary: %+v %v", acts, err)
	}
	assignments, err := store.ListAssignments(ready.RunID)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("assignments: %+v %v", assignments, err)
	}
	a := assignments[0]
	before, err := store.WorkerResult(a.AttemptID)
	if err != nil || len(before) == 0 {
		t.Fatalf("result not committed before kill: %s %v", before, err)
	}
	c := api.NewCoordinator(store)
	blobs, err := artifacts.NewLocal(cfg.Blobs)
	if err != nil {
		t.Fatal(err)
	}
	c.Artifacts = blobs
	engines, err := plan.RecoverRuns(store, c)
	if err != nil || engines[ready.RunID] == nil {
		t.Fatalf("recovery: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	engines[ready.RunID].Context = ctx
	if err := engines[ready.RunID].Run(ready.RunID); err != nil {
		t.Fatal(err)
	}
	// A delayed old-epoch message cannot replace the committed output.
	if err := c.Complete(a.WorkerID, api.WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation, State: "succeeded", Outputs: map[string]any{"file": "stale"}}); err != nil {
		t.Fatal(err)
	}
	after, err := store.WorkerResult(a.AttemptID)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("delayed message changed committed result: %s %v", after, err)
	}
	run, err := store.GetRun(ready.RunID)
	if err != nil || run.State != plan.RunSucceeded {
		t.Fatalf("run: %+v %v", run, err)
	}
	acts, err = store.ListActivations(ready.RunID)
	if err != nil || len(acts) != 1 {
		t.Fatalf("activations: %+v %v", acts, err)
	}
	lifecycleArtifact(t, blobs, acts[0].Outputs)
	lifecycleEffects(t, ledger, 1)
}

func TestLifecycleDroppedCompletionResponseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	store := lifecycleOpen(t, filepath.Join(dir, "state.db"))
	c := api.NewCoordinator(store)
	blobs, err := artifacts.NewLocal(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	c.Artifacts = blobs
	server := httptest.NewServer(api.NewRouterWithArtifacts(store, nil, blobs, api.NewWorkerProtocolHandler(c)))
	t.Cleanup(server.Close)
	var completions atomic.Int32
	var first []byte // only the completion goroutine accesses this
	retried := make(chan struct{})
	cfg := lifecycleWorkerConfig(server.URL, t.TempDir())
	cfg.HTTPClient = &http.Client{Transport: lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/complete") {
			return http.DefaultTransport.RoundTrip(req)
		}
		raw, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		n := completions.Add(1)
		if n == 1 {
			first = raw
		} else if !bytes.Equal(first, raw) {
			return nil, fmt.Errorf("retry changed completion envelope")
		}
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if n == 1 {
			resp.Body.Close()
			return nil, io.ErrUnexpectedEOF // coordinator committed; acknowledgement lost
		}
		if n == 2 {
			close(retried)
		}
		return resp, nil
	})}
	adapters, ledger := lifecycleScript(t)
	stop := lifecycleWorker(t, cfg, workers.NewScriptExecutor(adapters, t.TempDir()))
	engine := plan.NewEngineWithStore(lifecyclePlan(), c, store)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	engine.Context = ctx
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retried:
	case <-ctx.Done():
		t.Fatal("completion was not retried after lost acknowledgement")
	}
	stop()
	if completions.Load() != 2 {
		t.Fatalf("completions=%d", completions.Load())
	}
	acts, err := store.ListActivations(id)
	if err != nil || len(acts) != 1 || acts[0].State != plan.ActSucceeded {
		t.Fatalf("duplicate corrupted activation: %+v %v", acts, err)
	}
	attempts, err := store.ListExecutionAttempts(id, "")
	if err != nil || len(attempts) != 1 || attempts[0].State != "succeeded" {
		t.Fatalf("duplicate created attempts: %+v %v", attempts, err)
	}
	lifecycleArtifact(t, blobs, acts[0].Outputs)
	lifecycleEffects(t, ledger, 1)
}

func TestLifecycleKilledWorkerLeavesUncertainEffect(t *testing.T) {
	dir := t.TempDir()
	store := lifecycleOpen(t, filepath.Join(dir, "state.db"))
	c := api.NewCoordinator(store)
	server := httptest.NewServer(api.NewWorkerProtocolHandler(c))
	t.Cleanup(server.Close)
	adapters, ledger := lifecycleScript(t)
	boundary := filepath.Join(dir, "effect-completed")
	kill := lifecycleStartProcess(t, lifecycleProcessConfig{Kind: "worker", URL: server.URL, Adapters: adapters, Work: t.TempDir(), Boundary: boundary})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := plan.NewEngineWithStore(lifecyclePlan(), c, store)
	engine.Context = ctx
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run(id) }()
	engineStopped := false
	stopEngine := func() {
		if engineStopped {
			return
		}
		engineStopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("engine did not stop")
		}
	}
	t.Cleanup(stopEngine)
	lifecycleWait(t, "worker side effect", func() bool { _, err := os.Stat(boundary); return err == nil })
	kill()
	// Replacing the worker process revokes its lease without waiting for wall time.
	if _, err := c.Register(api.WorkerRegistration{WorkerID: "fault-worker", InstanceID: "replacement", Capacity: 2, Capabilities: []api.WorkerCapability{lifecycleCapability()}}); err != nil {
		t.Fatal(err)
	}
	assignments, err := store.ListAssignments(id)
	if err != nil || len(assignments) != 1 || assignments[0].State != "uncertain" {
		t.Fatalf("lost side effect uncertainty: %+v %v", assignments, err)
	}
	a := assignments[0]
	// A result from the revoked generation is diagnostic evidence, not proof
	// that the current attempt may be committed or the side effect replayed.
	if err := c.Complete(a.WorkerID, api.WorkerCompletion{AssignmentID: a.AssignmentID, RunID: a.RunID, ActivationID: a.ActivationID, AttemptID: a.AttemptID, CoordinatorEpoch: a.CoordinatorEpoch, Generation: a.Generation - 1, State: "succeeded", Outputs: map[string]any{"file": "delayed"}}); err != nil {
		t.Fatal(err)
	}
	if raw, err := store.WorkerDiagnostic(a.AttemptID); err != nil || !bytes.Contains(raw, []byte("delayed")) {
		t.Fatalf("delayed evidence lost: %s %v", raw, err)
	}
	if next, err := c.NextAssignmentForInstance("fault-worker", "replacement"); err != nil || next != nil {
		t.Fatalf("uncertain side effect replayed: %+v %v", next, err)
	}
	if raw, err := store.WorkerResult(a.AttemptID); err != nil || len(raw) != 0 {
		t.Fatalf("invented worker result: %s %v", raw, err)
	}
	select {
	case err := <-done:
		done <- err
		t.Fatalf("uncertain execution incorrectly terminated: %v", err)
	default:
	}
	stopEngine()
	server.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = lifecycleOpen(t, filepath.Join(dir, "state.db"))
	c = api.NewCoordinator(store)
	engines, err := plan.RecoverRuns(store, c)
	if err != nil || engines[id] == nil {
		t.Fatalf("uncertain run lost on restart: %v", err)
	}
	assignments, err = store.ListAssignments(id)
	if err != nil || len(assignments) != 1 || assignments[0].State != "uncertain" {
		t.Fatalf("uncertainty lost on restart: %+v %v", assignments, err)
	}
	if raw, err := store.WorkerResult(a.AttemptID); err != nil || len(raw) != 0 {
		t.Fatalf("restart promoted delayed result: %s %v", raw, err)
	}
	if raw, err := store.WorkerDiagnostic(a.AttemptID); err != nil || !bytes.Contains(raw, []byte("delayed")) {
		t.Fatalf("delayed evidence lost on restart: %s %v", raw, err)
	}
	if _, err := c.Register(api.WorkerRegistration{WorkerID: "fault-worker", InstanceID: "after-restart", Capacity: 2, Capabilities: []api.WorkerCapability{lifecycleCapability()}}); err != nil {
		t.Fatal(err)
	}
	if next, err := c.NextAssignmentForInstance("fault-worker", "after-restart"); err != nil || next != nil {
		t.Fatalf("restart replayed uncertain side effect: %+v %v", next, err)
	}
	lifecycleEffects(t, ledger, 1)
}

// Returning a body read error makes net/http break the upload connection after
// transmitting a prefix. The real API and disk driver must discard staging data.
type lifecycleBrokenBody struct {
	io.ReadCloser
	remaining int
}

func (b *lifecycleBrokenBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= n
	return n, err
}

func TestLifecycleInterruptedArtifactUploadNeverCommitsOutput(t *testing.T) {
	t.Run("transport_disconnect", func(t *testing.T) { lifecycleInterruptedUpload(t, false) })
	t.Run("server_partial_read", func(t *testing.T) { lifecycleInterruptedUpload(t, true) })
}

func lifecycleInterruptedUpload(t *testing.T, serverReadFault bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	store := lifecycleOpen(t, path)
	c := api.NewCoordinator(store)
	blobs, err := artifacts.NewLocal(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	c.Artifacts = blobs
	router := api.NewRouterWithArtifacts(store, nil, blobs, api.NewWorkerProtocolHandler(c))
	var interrupted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if serverReadFault && req.Method == http.MethodPut && strings.HasPrefix(req.URL.Path, "/v1/artifacts/") {
			// A client-side write error can occur before net/http flushes any
			// bytes. Also force a read failure inside the real artifact handler
			// so cleanup of an actual five-byte staging file is exercised.
			body := &lifecycleBrokenBody{ReadCloser: req.Body, remaining: 5}
			req.Body = body
			router.ServeHTTP(w, req)
			if body.remaining == 0 {
				interrupted.Add(1)
			}
			return
		}
		router.ServeHTTP(w, req)
	}))
	t.Cleanup(server.Close)
	cfg := lifecycleWorkerConfig(server.URL, t.TempDir())
	cfg.HTTPClient = &http.Client{Transport: lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		if !serverReadFault && req.Method == http.MethodPut && strings.HasPrefix(req.URL.Path, "/v1/artifacts/") {
			interrupted.Add(1)
			req.Body = &lifecycleBrokenBody{ReadCloser: req.Body, remaining: 5}
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	adapters, ledger := lifecycleScript(t)
	stop := lifecycleWorker(t, cfg, workers.NewScriptExecutor(adapters, t.TempDir()))
	engine := plan.NewEngineWithStore(lifecyclePlan(), c, store)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	engine.Context = ctx
	id, err := engine.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(id); err == nil {
		t.Fatal("interrupted upload reported success")
	}
	stop()
	if interrupted.Load() < 2 {
		t.Fatalf("snapshot and final publication faults not both reached: uploads=%d", interrupted.Load())
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("committed artifact")))
	if blobs.Has(digest) {
		t.Fatal("partial upload became a committed artifact")
	}
	if serverReadFault {
		entries, err := os.ReadDir(filepath.Join(dir, "blobs"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("interrupted upload left staging data or partial objects: %v %v", entries, err)
		}
	}
	acts, err := store.ListActivations(id)
	if err != nil || len(acts) != 1 || acts[0].State == plan.ActSucceeded || len(acts[0].Outputs) != 0 {
		t.Fatalf("incomplete artifact published: %+v %v", acts, err)
	}
	// A later transfer of the same digest must work; a partial object must not
	// occupy the content address or poison an otherwise recoverable artifact.
	client := artifacts.Client{BaseURL: server.URL}
	if serverReadFault {
		// Use a healthy endpoint backed by the same driver for the repair.
		healthy := httptest.NewServer(router)
		t.Cleanup(healthy.Close)
		client.BaseURL = healthy.URL
	}
	if _, err := client.Upload(ctx, digest, "text/plain", 18, strings.NewReader("committed artifact")); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = lifecycleOpen(t, path)
	engines, err := plan.RecoverRuns(store, api.NewCoordinator(store))
	if err != nil || engines[id] != nil {
		t.Fatalf("failed upload was automatically replayed: %v", err)
	}
	run, err := store.GetRun(id)
	if err != nil || run.State != plan.RunFailed || !strings.Contains(run.Error, "upload") {
		t.Fatalf("upload failure lost on restart: %+v %v", run, err)
	}
	if !blobs.Has(digest) {
		t.Fatal("subsequent committed artifact lost")
	}
	lifecycleEffects(t, ledger, 1)
}

func TestLifecycleRestartWithOnePausedMapChild(t *testing.T) {
	dir := t.TempDir()
	p := lifecyclePlan()
	work := p.Subgraphs["root"].Nodes[0]
	work.Retry = &plan.RetryPolicy{Effect: "none"} // explicitly safe to replay an uncommitted child
	work.Inputs = map[string]plan.Binding{"item": {Expr: &plan.Expr{Op: "item"}}}
	concurrency := 2
	p.Subgraphs["root"] = plan.Subgraph{Nodes: []plan.Node{{ID: "fanout", Kind: plan.KindMap, BodyRef: "body", Items: &plan.Expr{Op: "literal", Value: json.RawMessage(`[0,1]`)}, Concurrency: &concurrency}}}
	p.Subgraphs["body"] = plan.Subgraph{Nodes: []plan.Node{work}}
	cfg := lifecycleProcessConfig{Kind: "coordinator", DB: filepath.Join(dir, "state.db"), Blobs: filepath.Join(dir, "blobs"), Ready: filepath.Join(dir, "ready"), Plan: p}
	kill := lifecycleStartProcess(t, cfg)
	var ready lifecycleReady
	lifecycleWait(t, "map coordinator startup", func() bool {
		raw, _ := os.ReadFile(cfg.Ready)
		return json.Unmarshal(raw, &ready) == nil
	})
	adapters, ledger := lifecycleScript(t)
	inner := workers.NewScriptExecutor(adapters, t.TempDir())
	paused := make(chan struct{}, 1)
	gate := lifecycleExecutor(func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		if req.Inputs["item"] == float64(1) {
			paused <- struct{}{}
			<-req.Context.Done()
			return workers.ExecuteResult{}, req.Context.Err()
		}
		return inner.Execute(req)
	})
	stop := lifecycleWorker(t, lifecycleWorkerConfig(ready.URL, t.TempDir()), gate)
	store := lifecycleOpen(t, cfg.DB)
	var committedID string
	lifecycleWait(t, "first map child checkpoint", func() bool {
		acts, err := store.ListActivations(ready.RunID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range acts {
			if a.NodeID == "work" && a.State == plan.ActSucceeded {
				committedID = a.ID
				return true
			}
		}
		return false
	})
	select {
	case <-paused:
	case <-time.After(3 * time.Second):
		t.Fatal("second map child was never paused")
	}
	beforeCrash, err := store.ListActivations(ready.RunID)
	if err != nil || len(beforeCrash) != 3 {
		t.Fatalf("map activations before crash: %+v %v", beforeCrash, err)
	}
	identities := make(map[string]bool, len(beforeCrash))
	var committedOutputs []byte
	for _, a := range beforeCrash {
		identities[a.ID] = true
		if a.ID == committedID {
			committedOutputs, err = json.Marshal(a.Outputs)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	lifecycleEffects(t, ledger, 1)
	kill()
	stop()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = lifecycleOpen(t, cfg.DB)
	c := api.NewCoordinator(store)
	blobs, err := artifacts.NewLocal(cfg.Blobs)
	if err != nil {
		t.Fatal(err)
	}
	c.Artifacts = blobs
	engines, err := plan.RecoverRuns(store, c)
	if err != nil || engines[ready.RunID] == nil {
		t.Fatalf("map recovery: %v", err)
	}
	server := httptest.NewServer(api.NewRouterWithArtifacts(store, nil, blobs, api.NewWorkerProtocolHandler(c)))
	t.Cleanup(server.Close)
	stopRecovered := lifecycleWorker(t, lifecycleWorkerConfig(server.URL, t.TempDir()), workers.NewScriptExecutor(adapters, t.TempDir()))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	engines[ready.RunID].Context = ctx
	if err := engines[ready.RunID].Run(ready.RunID); err != nil {
		t.Fatal(err)
	}
	stopRecovered()
	acts, err := store.ListActivations(ready.RunID)
	if err != nil || len(acts) != 3 {
		t.Fatalf("map identities changed: %+v %v", acts, err)
	}
	for _, a := range acts {
		if !identities[a.ID] {
			t.Fatalf("recovery replaced activation identity: %+v", a)
		}
		if a.State != plan.ActSucceeded {
			t.Fatalf("map activation did not finish: %+v", a)
		}
		if a.NodeID == "work" {
			lifecycleArtifact(t, blobs, a.Outputs)
		}
		if a.ID == committedID {
			raw, err := json.Marshal(a.Outputs)
			if err != nil || !bytes.Equal(raw, committedOutputs) {
				t.Fatalf("recovery changed committed child output: %s %v", raw, err)
			}
		}
	}
	attempts, err := store.ListExecutionAttempts(ready.RunID, committedID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("committed child was retried: %+v %v", attempts, err)
	}
	run, err := store.GetRun(ready.RunID)
	if err != nil || run.State != plan.RunSucceeded {
		t.Fatalf("map run: %+v %v", run, err)
	}
	lifecycleEffects(t, ledger, 2)
}
