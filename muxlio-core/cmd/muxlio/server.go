package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/coordinatorlog"
	"github.com/muxlio/muxlio-core/internal/mcp"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/subscription"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type serverRunner struct {
	store     *sqlite.Store
	exec      workers.Executor
	artifacts artifacts.Driver
	mu        sync.Mutex
	engines   map[string]*plan.Engine
	workers   map[string]chan struct{}
	ctx       context.Context
	wg        sync.WaitGroup
}

// PreflightRun validates against the coordinator used for actual submission.
func (r *serverRunner) PreflightRun(req api.PreflightRequest) api.PreflightResponse {
	if c, ok := r.exec.(*api.Coordinator); ok {
		return c.PreflightRun(req)
	}
	resp := api.PreflightRun(&req.Plan, req.Input, r.store)
	if req.WorkspaceID != "" || req.Plan.WorkspaceID != "" {
		resp.Feasible = false
		resp.WorkspaceError = "workspace validation requires a coordinator"
	}
	return resp
}

func (r *serverRunner) CreateRun(input json.RawMessage) (string, error) {
	return "", errors.New("a plan is required to create a run")
}

func (r *serverRunner) CreateWorkspacePlanRun(p *plan.Plan, input json.RawMessage, id string) (string, error) {
	if err := p.VerifyDigest(); err != nil {
		return "", err
	}
	copy := *p
	if copy.WorkspaceID != "" && copy.WorkspaceID != id {
		// Compare authorized identities, since either selector may be an alias.
		c, ok := r.exec.(*api.Coordinator)
		if !ok {
			return "", api.WorkspaceAuthorizationError{Message: "submission and plan workspace IDs disagree"}
		}
		if err := c.AuthorizePlanWorkspace(&copy, input); err != nil {
			return "", err
		}
		selected := copy
		selected.WorkspaceID = id
		if _, err := selected.ComputeDigest(); err != nil {
			return "", err
		}
		if err := c.AuthorizePlanWorkspace(&selected, input); err != nil {
			return "", err
		}
		if copy.WorkspaceID != selected.WorkspaceID {
			return "", api.WorkspaceAuthorizationError{Message: "submission and plan workspace IDs disagree"}
		}
	}
	copy.WorkspaceID = id
	if _, err := copy.ComputeDigest(); err != nil {
		return "", err
	}
	return r.CreatePlanRun(&copy, input)
}

func (r *serverRunner) CreatePlanRun(p *plan.Plan, input json.RawMessage) (string, error) {
	if c, ok := r.exec.(*api.Coordinator); ok {
		if err := c.AuthorizePlanWorkspace(p, input); err != nil {
			return "", err
		}
	}
	engine := plan.NewEngineWithStore(p, r.exec, r.store)
	engine.Context = r.ctx
	runID, err := engine.CreateRun(input)
	if err != nil {
		return "", err
	}
	coordinatorlog.RunAccepted(runID, p.Name)
	r.mu.Lock()
	r.engines[runID] = engine
	r.workers[runID] = make(chan struct{})
	r.wg.Add(1)
	r.mu.Unlock()
	return runID, nil
}

// CreateIdempotentRun implements api.idempotentRunner. It creates a run while
// atomically deduplicating concurrent or retried submissions with the same key.
func (r *serverRunner) CreateIdempotentRun(p *plan.Plan, input json.RawMessage, workspaceID, idempotencyKey string) (string, bool, error) {
	return r.createIdempotentRun(p, input, workspaceID, idempotencyKey, state.SubmissionMetadata{})
}

func (r *serverRunner) createIdempotentRun(p *plan.Plan, input json.RawMessage, workspaceID, idempotencyKey string, metadata state.SubmissionMetadata) (string, bool, error) {
	// Normalize: apply workspace authorization and compute the plan digest the
	// same way CreateWorkspacePlanRun / CreatePlanRun would, so the binding is
	// consistent regardless of whether a workspaceID is provided.
	if err := p.VerifyDigest(); err != nil {
		return "", false, err
	}
	pcopy := *p
	if workspaceID != "" {
		if pcopy.WorkspaceID != "" && pcopy.WorkspaceID != workspaceID {
			c, ok := r.exec.(*api.Coordinator)
			if !ok {
				return "", false, api.WorkspaceAuthorizationError{Message: "submission and plan workspace IDs disagree"}
			}
			if err := c.AuthorizePlanWorkspace(&pcopy, input); err != nil {
				return "", false, err
			}
			selected := pcopy
			selected.WorkspaceID = workspaceID
			if _, err := selected.ComputeDigest(); err != nil {
				return "", false, err
			}
			if err := c.AuthorizePlanWorkspace(&selected, input); err != nil {
				return "", false, err
			}
			if pcopy.WorkspaceID != selected.WorkspaceID {
				return "", false, api.WorkspaceAuthorizationError{Message: "submission and plan workspace IDs disagree"}
			}
		}
		pcopy.WorkspaceID = workspaceID
	}
	// Recompute only after verifying the caller digest, then authorize every selector.
	if _, err := pcopy.ComputeDigest(); err != nil {
		return "", false, err
	}
	if c, ok := r.exec.(*api.Coordinator); ok {
		if err := c.AuthorizePlanWorkspace(&pcopy, input); err != nil {
			return "", false, err
		}
	}
	if _, err := pcopy.ComputeDigest(); err != nil {
		return "", false, err
	}
	// Bind the exact input bytes without converting JSON numbers through float64.
	rawInput := input
	if len(rawInput) == 0 {
		rawInput = json.RawMessage(`{}`)
	}
	h := sha256.Sum256(rawInput)
	inputDigest := hex.EncodeToString(h[:])

	// Delegate actual run creation to the Engine so plan validation, input
	// validation, and plan pinning happen exactly as in CreatePlanRun.
	engine := plan.NewEngineWithStore(&pcopy, r.exec, r.store)
	engine.Context = r.ctx
	engine.SubmissionMetadata = metadata
	runID, isDuplicate, err := engine.CreateRunIdempotent(rawInput, idempotencyKey, inputDigest)
	if err != nil {
		return "", false, err
	}
	if isDuplicate {
		// Existing run returned without creating or running a duplicate.
		return runID, true, nil
	}
	r.mu.Lock()
	r.engines[runID] = engine
	r.workers[runID] = make(chan struct{})
	r.wg.Add(1)
	r.mu.Unlock()
	coordinatorlog.RunAccepted(runID, pcopy.Name)
	return runID, false, nil
}

// CreateRegisteredRun authorizes a separate workspace selector without rewriting
// the immutable definition. Execution and restart read the workspace from the run.
func (r *serverRunner) CreateRegisteredRun(raw []byte, input json.RawMessage, workspaceID, key string) (string, bool, error) {
	return r.createRegisteredRun(raw, input, workspaceID, key, state.SubmissionMetadata{})
}

func (r *serverRunner) createRegisteredRun(raw []byte, input json.RawMessage, workspaceID, key string, metadata state.SubmissionMetadata) (string, bool, error) {
	var p plan.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", false, err
	}
	if err := p.VerifyDigest(); err != nil {
		return "", false, err
	}
	selected := p
	if workspaceID != "" {
		selected.WorkspaceID = workspaceID
		if _, err := selected.ComputeDigest(); err != nil {
			return "", false, err
		}
	}
	if c, ok := r.exec.(*api.Coordinator); ok {
		if err := c.AuthorizePlanWorkspace(&selected, input); err != nil {
			return "", false, err
		}
		if p.WorkspaceID != "" {
			original := p
			if err := c.AuthorizePlanWorkspace(&original, input); err != nil {
				return "", false, err
			}
			if original.WorkspaceID != selected.WorkspaceID {
				return "", false, api.WorkspaceAuthorizationError{Message: "submission and plan workspace IDs disagree"}
			}
		}
	} else if selected.WorkspaceID != "" {
		return "", false, api.WorkspaceAuthorizationError{Message: "workspace validation requires a coordinator"}
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	h := sha256.Sum256(input)
	engine := plan.NewEngineWithStore(&p, r.exec, r.store)
	engine.Context = r.ctx
	engine.SubmissionMetadata = metadata
	id, duplicate, err := engine.CreateRegisteredRun(raw, input, selected.WorkspaceID, key, hex.EncodeToString(h[:]))
	if err != nil || duplicate {
		return id, duplicate, err
	}
	r.mu.Lock()
	r.engines[id] = engine
	r.workers[id] = make(chan struct{})
	r.wg.Add(1)
	r.mu.Unlock()
	coordinatorlog.RunAccepted(id, p.Name)
	return id, false, nil
}

// StartPersistedRun attaches an engine to an already atomically persisted run.
func (r *serverRunner) StartPersistedRun(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.engines[id] != nil || r.ctx.Err() != nil {
		return nil
	}
	run, err := r.store.GetRun(id)
	if err != nil {
		return err
	}
	if run.State != plan.RunCreated && run.State != plan.RunRunning {
		return nil
	}
	var p plan.Plan
	if err := json.Unmarshal(run.PlanJSON, &p); err != nil {
		return err
	}
	if err := p.VerifyDigest(); err != nil {
		return err
	}
	engine := plan.NewEngineWithStore(&p, r.exec, r.store)
	engine.Context = r.ctx
	r.engines[id] = engine
	r.workers[id] = make(chan struct{})
	r.wg.Add(1)
	coordinatorlog.RunAccepted(id, p.Name)
	go func() { _ = r.Run(id) }()
	return nil
}

func (r *serverRunner) runSchedules() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if r.ctx.Err() != nil {
			return
		}
		ids, err := r.store.TickSchedules(time.Now().UTC())
		if err != nil {
			fmt.Fprintln(os.Stderr, "schedule tick:", err)
		}
		for _, id := range ids {
			if err := r.StartPersistedRun(id); err != nil {
				fmt.Fprintln(os.Stderr, "schedule launch:", err)
			}
		}
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *serverRunner) Run(runID string) error {
	r.mu.Lock()
	engine := r.engines[runID]
	done := r.workers[runID]
	r.mu.Unlock()
	if engine == nil {
		return errors.New("run engine not found")
	}
	if run, err := r.store.GetRun(runID); err == nil && run.State == plan.RunPaused {
		engine.Pause()
	}
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		// A retry may have installed a replacement engine while this worker
		// was unwinding. Only the worker that still owns the slot may clear it.
		if r.engines[runID] == engine {
			delete(r.engines, runID)
		}
		if done != nil {
			if r.workers[runID] == done {
				delete(r.workers, runID)
			}
			close(done)
		}
		r.mu.Unlock()
	}()
	return engine.Run(runID)
}

func (r *serverRunner) ControlRun(runID, action string) error {
	r.mu.Lock()
	engine := r.engines[runID]
	run, err := r.store.GetRun(runID)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	switch action {
	case "pause":
		if run.State != plan.RunCreated && run.State != plan.RunRunning && run.State != plan.RunWaiting {
			r.mu.Unlock()
			return fmt.Errorf("cannot pause run in state %q", run.State)
		}
		if engine != nil {
			engine.Pause()
		}
		changed, err := r.store.TransitionRunState(runID, []string{plan.RunCreated, plan.RunRunning, plan.RunWaiting}, plan.RunPaused)
		if err != nil {
			if engine != nil {
				engine.Resume()
			}
			r.mu.Unlock()
			return err
		}
		if !changed {
			if engine != nil {
				engine.Resume()
			}
			r.mu.Unlock()
			return errors.New("run state changed before pause could be applied")
		}
	case "resume":
		if run.State != plan.RunPaused {
			r.mu.Unlock()
			return fmt.Errorf("cannot resume run in state %q", run.State)
		}
		changed, err := r.store.TransitionRunState(runID, []string{plan.RunPaused}, plan.RunRunning)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		if !changed {
			r.mu.Unlock()
			return errors.New("run state changed before resume could be applied")
		}
		if engine != nil {
			engine.Resume()
		} else {
			r.mu.Unlock()
			return r.StartPersistedRun(runID)
		}
	case "cancel":
		if run.State == plan.RunSucceeded || run.State == plan.RunFailed || run.State == plan.RunCancelled {
			r.mu.Unlock()
			return fmt.Errorf("cannot cancel run in state %q", run.State)
		}
		changed, err := r.store.TransitionRunState(runID, []string{plan.RunCreated, plan.RunRunning, plan.RunWaiting, plan.RunPaused}, plan.RunCancelled)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		if !changed {
			r.mu.Unlock()
			return errors.New("run state changed before cancellation could be applied")
		}
		if err := r.store.CancelRunWork(runID); err != nil {
			r.mu.Unlock()
			return fmt.Errorf("run cancelled but worker assignments could not be revoked: %w", err)
		}
		if engine != nil {
			engine.Cancel()
		}
	case "retry":
		if run.State != plan.RunFailed && run.State != plan.RunCancelled {
			r.mu.Unlock()
			return fmt.Errorf("cannot retry run in state %q; only failed or cancelled runs can be retried", run.State)
		}
		if done := r.workers[runID]; done != nil {
			r.mu.Unlock()
			<-done
			return r.ControlRun(runID, action)
		}
		// Build a fresh engine from a nil plan — Retry() loads the pinned plan
		// from the store, so the new engine is never tainted by the old one's
		// cancelled context.
		newEngine := plan.NewEngineWithStore(nil, r.exec, r.store)
		newEngine.Context = r.ctx
		if err := newEngine.Retry(runID); err != nil {
			r.mu.Unlock()
			return err
		}
		r.engines[runID] = newEngine
		r.workers[runID] = make(chan struct{})
		r.wg.Add(1)
		r.mu.Unlock()
		go func() { _ = r.Run(runID) }()
		return nil
	default:
		r.mu.Unlock()
		return fmt.Errorf("unknown run action %q", action)
	}
	r.mu.Unlock()
	return nil
}

// cmdServerStart opens the SQLite store and serves the HTTP API until
// SIGTERM or SIGINT is received.
func cmdServerStart(dataDir, addr string, jsonOut bool) int {
	return serve(dataDir, addr, jsonOut, nil)
}

func serve(dataDir, addr string, jsonOut bool, config *serviceConfig) int {
	shutdownTelemetry, telemetryErr := setupTelemetry()
	if telemetryErr != nil {
		fmt.Fprintln(os.Stderr, telemetryErr)
		return 1
	}
	defer shutdownTelemetry()

	if dataDir == "" {
		dataDir = defaultRuntime()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: creating data dir: %v\n", err)
		return 1
	}
	// Standalone server start and service start share the same coordinator lock.
	// service start already holds it before writing its PID metadata.
	if config == nil {
		lock, err := os.OpenFile(filepath.Join(dataDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			fmt.Fprintln(os.Stderr, "coordinator already running for runtime directory")
			return 1
		}
	}
	artifactDriver, err := artifacts.FromEnvironment(filepath.Join(dataDir, "artifacts"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: artifact storage configuration is invalid: %v\n", err)
		return 1
	}

	store, err := sqlite.Open(filepath.Join(dataDir, "data", "coordinator.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: opening store: %v\n", err)
		return 1
	}
	defer store.Close()
	if config != nil {
		if err := store.ConfigureAdmission(config.Admission); err != nil {
			fmt.Fprintf(os.Stderr, "error: admission configuration: %v\n", err)
			return 1
		}
	}

	coordinator := api.NewCoordinator(store)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	coordinator.Context = ctx
	if config != nil {
		coordinator.Workspaces = config.Workspaces
		coordinator.Workspace = config.Workspace
	} else {
		coordinator.Workspace = os.Getenv("MUXLIO_WORKSPACE")
	}
	// Pin local authority once at startup. A configured symlink must never
	// retarget an existing identity when the next client submits a run.
	localBindings, err := coordinator.Workspaces.LocalBindings("local")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: workspace configuration: %v\n", err)
		return 2
	}
	pinnedBindings := workers.WorkspaceRegistry{}
	for id, binding := range coordinator.Workspaces {
		pinnedBindings[id] = binding
	}
	for id, binding := range localBindings {
		pinnedBindings[id] = binding
	}
	coordinator.Workspaces = pinnedBindings
	if coordinator.Workspace != "" && len(pinnedBindings) == 0 {
		coordinator.Workspace, err = workers.CanonicalWorkspace(coordinator.Workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: workspace configuration: %v\n", err)
			return 2
		}
	}
	if config != nil {
		config.Workspaces, config.Workspace = pinnedBindings, coordinator.Workspace
	}
	coordinator.Artifacts = artifactDriver
	runner := &serverRunner{store: store, exec: coordinator, artifacts: artifactDriver, engines: make(map[string]*plan.Engine), workers: make(map[string]chan struct{}), ctx: ctx}
	recovered, err := plan.RecoverRuns(store, coordinator)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: recover:", err)
		return 1
	}
	runner.engines = recovered
	workerProtocol := api.NewWorkerProtocolHandler(coordinator)
	mux := api.NewRouterWithArtifacts(store, runner, artifactDriver, workerProtocol)
	handler := http.Handler(withRequestBodyLimit(mux))

	credentialConfigPath := filepath.Join(dataDir, "config.json")
	if config != nil && config.configPath != "" {
		credentialConfigPath = config.configPath
	}

	// Apply bearer token authentication if configured.
	// Token precedence: MUXLIO_TOKEN env, then the protected runtime config.
	authToken := loadToken("", "")
	if authToken == "" && config != nil {
		authToken = config.Token
	}
	if authToken == "" {
		token, tokenErr := ensureAuthToken(credentialConfigPath)
		if tokenErr != nil {
			fmt.Fprintf(os.Stderr, "error: configuring coordinator token: %v\n", tokenErr)
			return 1
		}
		authToken = token
	}
	workerToken := strings.TrimSpace(os.Getenv("MUXLIO_WORKER_TOKEN"))
	if workerToken == "" {
		var workerTokenErr error
		workerToken, workerTokenErr = ensureWorkerToken(credentialConfigPath)
		if workerTokenErr != nil {
			fmt.Fprintf(os.Stderr, "error: configuring worker token: %v\n", workerTokenErr)
			return 1
		}
	}
	_, grantErr := readAccessGrants(credentialConfigPath)
	if grantErr != nil {
		fmt.Fprintln(os.Stderr, "error: configuring scoped credentials:", grantErr)
		return 1
	}
	handler = api.WithReloadingAuth(handler, authToken, func() (string, []api.AccessGrant, error) {
		grants, err := readAccessGrants(credentialConfigPath)
		currentWorkerToken := strings.TrimSpace(os.Getenv("MUXLIO_WORKER_TOKEN"))
		if currentWorkerToken == "" {
			currentWorkerToken = readConfiguredWorkerToken(credentialConfigPath)
		}
		return currentWorkerToken, grants, err
	})

	if config != nil {
		handler = serviceUI(handler, config.UI, authToken)
	}
	var maintenanceGate sync.RWMutex
	handler = withMaintenanceGate(handler, &maintenanceGate)

	tlsCert, tlsKey := "", ""
	if config != nil {
		tlsCert, tlsKey = config.TLSCert, config.TLSKey
	} else {
		tlsCert = strings.TrimSpace(os.Getenv("MUXLIO_TLS_CERT"))
		tlsKey = strings.TrimSpace(os.Getenv("MUXLIO_TLS_KEY"))
	}
	servers, err := startCoordinatorServers(handler, addr, tlsCert, tlsKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer servers.loopback.Close()
	defer servers.httpSrv.Close()
	if servers.tlsSrv != nil {
		defer servers.tlsListen.Close()
		defer servers.tlsSrv.Close()
	}

	var mcpServer *http.Server
	var mcpListener net.Listener
	if config != nil && config.MCP != nil {
		mcp.Version = CurrentVersion
		mcpHandler, err := mcp.NewHTTPHandler(servers.loopbackURL(), authToken, config.MCP.TokenFile, config.MCP.AllowOrigins)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: MCP: %v\n", err)
			return 1
		}
		mcpListener, err = mcp.ListenHTTP(config.MCP.Listen, config.MCP.TLSCert, config.MCP.TLSKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: MCP: %v\n", err)
			return 1
		}
		defer mcpListener.Close()
		mcpServer = mcp.NewHTTPServer(mcpHandler)
		defer mcpServer.Close()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	servers.start()
	// Include every listener in the same failure/shutdown path.
	errCh := make(chan error, 3)
	go func() { errCh <- <-servers.errCh }()
	if mcpServer != nil {
		go func() { errCh <- mcpServer.Serve(mcpListener) }()
		scheme := "http"
		if config.MCP.TLSCert != "" {
			scheme = "https"
		}
		fmt.Fprintf(os.Stderr, "muxlio MCP listening on %s://%s/mcp\n", scheme, mcpListener.Addr())
	}
	for id, engine := range recovered {
		engine.Context = ctx
		runner.mu.Lock()
		runner.workers[id] = make(chan struct{})
		runner.mu.Unlock()
		runner.wg.Add(1)
		go func(id string) { _ = runner.Run(id) }(id)
	}
	subscriptionsDone := make(chan struct{})
	go func() {
		defer close(subscriptionsDone)
		(&subscription.Dispatcher{Store: store, Events: store}).Run(ctx)
	}()
	schedulesDone := make(chan struct{})
	go func() { defer close(schedulesDone); runner.runSchedules() }()
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		runRetentionMaintenance(ctx, store, artifactDriver, dataDir, &maintenanceGate)
	}()
	workerDone := make(chan struct{})
	if config != nil {
		executor := workers.NewScriptExecutor(config.Adapters, filepath.Join(dataDir, "work"))
		executor.Context = ctx
		executor.Workspaces = config.Workspaces
		executor.Workspace = config.Workspace
		executor.AgentHome = config.AgentHome
		executor.Executors = config.Executors
		executor.WorkerEnv = config.WorkerEnv
		isolationLabels, err := workers.IsolationLabels(config.IsolationBackends)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}

		if config.AttemptTimeoutSeconds > 0 {
			executor.Timeout = time.Duration(config.AttemptTimeoutSeconds) * time.Second
		}
		caps := make([]workers.RemoteCapability, 0, len(config.Capabilities))
		for _, name := range config.Capabilities {
			report := executor.Probe(ctx, name)
			descriptor, _ := executor.Descriptor(name)
			capability := workers.RemoteCapability{Name: name, Contract: descriptor, Readiness: &report}
			if descriptor != nil {
				capability.Version = descriptor.ExecutorVersion
			}
			caps = append(caps, capability)
			for _, c := range report.Failed() {
				fmt.Fprintf(os.Stderr, "executor %s: %s: %s\n", name, c.Name, c.Message)
			}
		}
		workerURL := servers.loopbackURL()
		go func() {
			defer close(workerDone)
			backoff := time.Second
			const maxBackoff = 30 * time.Second
			for {
				if ctx.Err() != nil {
					return
				}
				err := workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{CoordinatorURL: workerURL, WorkerID: "local", Labels: isolationLabels, Capacity: config.WorkerCapacity, CapacityLimit: func() int { return config.WorkerCapacity }, Capabilities: caps, Token: workerToken, WorkBase: filepath.Join(dataDir, "work"), Workspace: config.Workspace, Workspaces: config.Workspaces}, executor)
				if err == nil || ctx.Err() != nil {
					return
				}
				fmt.Fprintf(os.Stderr, "local worker stopped: %v; restarting in %s\n", err, backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < maxBackoff {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}
		}()
	} else {
		close(workerDone)
	}

	if jsonOut {
		payload := map[string]any{
			"status":  "running",
			"addr":    addr,
			"dataDir": dataDir,
		}
		if la := servers.loopbackAddr(); la != "" {
			payload["loopback"] = la
		}
		if ta := servers.tlsAddr(); ta != "" {
			payload["tls"] = ta
		}
		out, _ := json.Marshal(payload)
		fmt.Println(string(out))
	} else if ta := servers.tlsAddr(); ta != "" {
		fmt.Printf("muxlio coordinator listening on %s (tls) and %s (http)  data=%s\n", ta, servers.loopbackAddr(), dataDir)
	} else {
		fmt.Printf("muxlio coordinator listening on %s  data=%s\n", servers.loopbackAddr(), dataDir)
	}

	code := 0
	select {
	case sig := <-quit:
		if !jsonOut {
			fmt.Printf("\nshutting down (%s)…\n", sig)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "error: server: %v\n", err)
			code = 1
		}
	}
	stop()
	<-retentionDone
	<-schedulesDone
	<-subscriptionsDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if mcpServer != nil {
		if err := mcpServer.Shutdown(shutdownCtx); err != nil {
			_ = mcpServer.Close()
			code = 1
		}
	}
	servers.shutdown(shutdownCtx)
	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		return 1
	}
	enginesDone := make(chan struct{})
	go func() { runner.wg.Wait(); close(enginesDone) }()
	select {
	case <-enginesDone:
	case <-shutdownCtx.Done():
		return 1
	}
	return code
}

func serviceUI(next http.Handler, directory, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			// Loopback and same-origin private-LAN browsers receive automatic operator credentials.
			// Explicit client and worker credentials retain their scopes.
			r = r.Clone(r.Context())
			if token != "" && len(r.Header.Values("Authorization")) == 0 && api.LocalNetworkCredentialRequest(r) {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			next.ServeHTTP(w, r)
			return
		}
		name, ok := "", false
		cleanPath := path.Clean(r.URL.Path)
		if strings.HasPrefix(cleanPath, "/dist/") && cleanPath == r.URL.Path {
			switch path.Ext(cleanPath) {
			case ".js", ".css", ".map", ".png", ".ico":
				name, ok = strings.TrimPrefix(cleanPath, "/"), true
			}
		} else if r.URL.Path == "/logo.png" || r.URL.Path == "/favicon.ico" {
			name, ok = strings.TrimPrefix(r.URL.Path, "/"), true
		} else if isUIRoute(r.URL.EscapedPath()) {
			name, ok = "index.html", true
		}
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// UI URLs stay stable across upgrades, so browsers must fetch the
		// current HTML, scripts, and styles rather than reuse an older layout.
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(directory, name))
	})
}

// Artifact bodies use the transport limit; control requests retain the smaller
// limit so increasing artifact capacity does not expand control-plane inputs.
func withRequestBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(8 << 20)
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/artifacts/") {
			limit = artifacts.MaxTransferBytes
		}
		http.MaxBytesHandler(next, limit).ServeHTTP(w, r)
	})
}

// Only known browser routes receive the app shell. Unknown APIs/assets retain 404s.
func isUIRoute(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch parts[0] {
	case "runs":
		return len(parts) == 1 || (len(parts) == 2 && parts[1] != "")
	case "worker-capabilities":
		return (len(parts) == 3 || len(parts) == 4) && parts[1] != "" && parts[2] != ""
	case "transcripts":
		return len(parts) == 3 && parts[1] != "" && parts[2] != ""
	case "operations", "automation", "retention", "workers", "live", "workflows", "artifacts", "analytics", "compare", "evidence":
		return len(parts) == 1
	}
	return path == "/"
}

// CreateSubmittedRun persists discovery metadata atomically with the run.
func (r *serverRunner) CreateSubmittedRun(p *plan.Plan, pinned []byte, input json.RawMessage, workspaceID, key string, metadata state.SubmissionMetadata) (string, bool, error) {
	if pinned != nil {
		return r.createRegisteredRun(pinned, input, workspaceID, key, metadata)
	}
	return r.createIdempotentRun(p, input, workspaceID, key, metadata)
}

func (r *serverRunner) ResolveRunWorkspaceID(id string) (string, error) {
	if c, ok := r.exec.(*api.Coordinator); ok {
		return c.ResolveRunWorkspaceID(id)
	}
	return id, nil
}
