package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
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

func (r *serverRunner) CreateRun(input json.RawMessage) (string, error) {
	return "", errors.New("a plan is required to create a run")
}

func (r *serverRunner) CreatePlanRun(p *plan.Plan, input json.RawMessage) (string, error) {
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
		}
	case "cancel":
		if run.State == plan.RunSucceeded || run.State == plan.RunFailed || run.State == plan.RunCancelled {
			r.mu.Unlock()
			return fmt.Errorf("cannot cancel run in state %q", run.State)
		}
		if engine != nil {
			engine.Cancel()
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

	coordinator := api.NewCoordinator(store)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	coordinator.Context = ctx
	if config != nil {
		coordinator.Workspace = config.Workspace
	} else {
		coordinator.Workspace = os.Getenv("MUXLIO_WORKSPACE")
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
	handler := http.Handler(http.MaxBytesHandler(mux, 8<<20))

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
	handler = api.WithAuth(handler, authToken, workerToken)

	if config != nil {
		handler = serviceUI(handler, config.UI, authToken)
	}

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
	workerDone := make(chan struct{})
	if config != nil {
		executor := workers.NewScriptExecutor(config.Adapters, filepath.Join(dataDir, "work"))
		executor.Context = ctx
		executor.Workspace = config.Workspace
		executor.AgentHome = config.AgentHome
		executor.Executors = config.Executors
		executor.WorkerEnv = config.WorkerEnv
		if config.AttemptTimeoutSeconds > 0 {
			executor.Timeout = time.Duration(config.AttemptTimeoutSeconds) * time.Second
		}
		caps := make([]workers.RemoteCapability, 0, len(config.Capabilities))
		for _, name := range config.Capabilities {
			allowed, err := executor.CanExecute(name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "executor %q: %v\n", name, err)
				return 2
			}
			if !allowed {
				continue
			}
			caps = append(caps, workers.RemoteCapability{Name: name})
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
				err := workers.RunRemoteWorker(ctx, workers.RemoteWorkerConfig{CoordinatorURL: workerURL, WorkerID: "local", Capacity: 1, Capabilities: caps, Token: workerToken, WorkBase: filepath.Join(dataDir, "work"), Workspace: config.Workspace}, executor)
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

func serviceUI(api http.Handler, directory, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_ui-api/v1/") {
			if token != "" {
				r = r.Clone(r.Context())
				r.Header = r.Header.Clone()
				r.Header.Set("Authorization", "Bearer "+token)
			}
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/_ui-api")
			api.ServeHTTP(w, r)
			return
		}
		assets := map[string]string{
			"/":                 "index.html",
			"/app.css":          "app.css",
			"/workspace.js":     "workspace.js",
			"/workspace.css":    "workspace.css",
			"/js/markdown.js":   "js/markdown.js",
			"/js/core.js":       "js/core.js",
			"/js/workbench.js":  "js/workbench.js",
			"/js/inspector.js":  "js/inspector.js",
			"/js/live.js":       "js/live.js",
			"/js/transcript.js": "js/transcript.js",
			"/js/analytics.js":  "js/analytics.js",
			"/js/boot.js":       "js/boot.js",
			"/logo.png":         "logo.png",
			"/favicon.ico":      "favicon.ico",
		}
		name, ok := assets[r.URL.Path]
		if !ok {
			api.ServeHTTP(w, r)
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
