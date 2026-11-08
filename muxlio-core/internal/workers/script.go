package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/runtimefiles"
	"github.com/muxlio/muxlio-core/internal/sandbox"
	"github.com/muxlio/muxlio-core/internal/telemetry"
)

// ScriptExecutor dispatches execute requests to per-executor shell scripts.
//
// Executor names map to executable filenames by replacing "/" with "-".
//
// The script receives a JSON object on stdin:
//
//	{ ...ExecuteRequest fields..., "workDir": "/path/to/scratch" }
//
// The script must write an ExecuteResult JSON object to stdout:
//
//	{ "outputs": { "portName": <any> }, "error": "" }
//
// If a declared output port has a "path" in its OutputDecl and the script
// wrote that file to workDir, ScriptExecutor reads it automatically and
// populates the port with { "mime", "path", "data" } if the script didn't
// already set it.
//
// Non-zero exit or invalid stdout JSON is treated as an executor error
// (returned in ExecuteResult.Error, not as a Go error).
type ScriptExecutor struct {
	WorkerID        string
	sessionInstance string
	adapterDir      string
	workBase        string
	Workspace       string
	// AgentHome sets HOME for native executor processes. Isolated processes
	// always keep their scratch HOME.
	AgentHome string
	Timeout   time.Duration
	Context   context.Context
	// Executors contains local operator permissions and environment settings.
	Executors map[string]ExecutorConfig
	// WorkerEnv supplies shared native execution settings, below executor overrides.
	WorkerEnv map[string]string
}

// isolationPolicy uses the shared sandbox contract.
type isolationPolicy = sandbox.Policy

// NewScriptExecutor creates a ScriptExecutor.
//   - adapterDir: directory containing adapter scripts (e.g. ~/.muxlio/adapters)
//   - workBase:   base directory for per-invocation scratch dirs (e.g. ~/.muxlio/work)
func NewScriptExecutor(adapterDir, workBase string) *ScriptExecutor {
	// Resolve to absolute path so exec.Command works regardless of process cwd.
	if abs, err := filepath.Abs(adapterDir); err == nil {
		adapterDir = abs
	}
	if abs, err := filepath.Abs(workBase); err == nil {
		workBase = abs
	}
	return &ScriptExecutor{adapterDir: adapterDir, workBase: workBase, sessionInstance: newSessionInstance()}
}

// NewAttempt returns a runner with independent lifecycle settings. Operator
// configuration is immutable for the lifetime of a worker.
func (s *ScriptExecutor) NewAttempt() Executor {
	clone := *s
	return &clone
}

// scriptPath converts an executor name to an absolute script path.
func (s *ScriptExecutor) scriptPath(executor string) string {
	name := strings.ReplaceAll(executor, "/", "-")
	return filepath.Join(s.adapterDir, name)
}

// Execute implements Executor.
func (s *ScriptExecutor) Execute(req ExecuteRequest) (result ExecuteResult, executionErr error) {
	if req.Context == nil {
		req.Context = s.Context
	}
	ctx, end := telemetry.Start(req.Context, "execute_tool", telemetry.AttemptAttributes(req.RunID, req.ActivationID, req.AttemptID, req.Executor)...)
	req.Context = ctx
	defer func() {
		telemetry.Usage(ctx, result.Usage)
		end(executionErr != nil || result.Error != "")
	}()
	started := time.Now()
	req, resolveErr := s.resolveSecrets(req)
	if resolveErr != nil {
		return ExecuteResult{Error: resolveErr.Error()}, nil
	}
	result, err := s.executeSession(req)
	telemetry.Duration(ctx, "muxlio.adapter.duration", time.Since(started), req.Executor)
	result, err = sanitizeExecution(result, err, executionSecrets(req))
	elapsed := time.Since(started).Milliseconds()
	result.ExecutionDurationMS = &elapsed
	return result, err
}

func (s *ScriptExecutor) execute(req ExecuteRequest) (ExecuteResult, error) {
	if err := ValidateWorkspaceContract(req.WorkspaceMode, req.ConcurrencyKeys); err != nil {
		return ExecuteResult{Error: err.Error()}, nil
	}
	if len(req.ConcurrencyKeys) > 0 && !req.resourcesAdmitted {
		return ExecuteResult{Error: "exclusive concurrency keys require coordinator worker admission; submit this task to a coordinator"}, nil
	}
	var workspaceErr error
	req, workspaceErr = ApplyWorkerWorkspace(req, s.Workspace)
	if workspaceErr != nil {
		return ExecuteResult{Error: workspaceErr.Error()}, nil
	}
	var guardErr error
	req, guardErr = GuardRequest(req)
	if guardErr != nil {
		return ExecuteResult{Error: guardErr.Error()}, nil
	}
	allowed, policyErr := s.CanExecute(req.Executor)
	if policyErr != nil {
		return ExecuteResult{}, policyErr
	}
	if !allowed {
		return ExecuteResult{Error: "executor host execution denied by worker policy"}, nil
	}
	script := s.scriptPath(req.Executor)
	if _, err := os.Stat(script); err != nil {
		d := diagnostic.New("MUXLIO_ADAPTER_NOT_FOUND", "runtime", fmt.Sprintf("no adapter for executor %q (expected script at %s)", req.Executor, script), "Install the executor adapter in the configured adapters directory or correct the executor name.")
		d.Expected = script
		d.Actual = "adapter unavailable"
		return ExecuteResult{}, &d
	}

	// Verify adapter capabilities before dispatch. Unsupported capabilities are
	// reported as explicit executor errors, never silently emulated.
	manifest, manifestErr := loadAdapterManifest(script)
	if manifestErr != nil {
		return ExecuteResult{Error: manifestErr.Error()}, nil
	}

	if capErr := CheckAdapterCapabilities(req, manifest); capErr != nil {
		d := diagnostic.New("MUXLIO_INCOMPATIBLE_REQUIREMENTS", "runtime", capErr.Error(), "Select an adapter whose descriptor satisfies the requested contract, versions, and capabilities, or correct the node requirements.")
		d.Requirements = map[string]any{"contractVersion": req.ContractVersion, "executorVersion": req.ExecutorVersion, "capabilities": req.RequiredCapabilities, "environment": req.Environment, "sessionRequested": req.SessionID != "", "remoteTaskRequested": req.RemoteTask != nil}
		d.Expected = d.Requirements
		d.Actual = manifest
		d.Causes = []diagnostic.Diagnostic{diagnostic.New("MUXLIO_CAUSE", "runtime", capErr.Error(), "")}
		return ExecuteResult{}, &d
	}

	// Keep each attempt's workspace under the run so incidental agent files
	// (notes, scratch output, CLI diagnostics) cannot land in the source repo.
	// Older/direct callers without execution IDs retain an isolated temp dir.
	workDir := req.attemptScratch
	if workDir == "" {
		var err error
		workDir, err = newAttemptScratch(req, s.workBase)
		if err != nil {
			return ExecuteResult{}, err
		}
	}

	req, err := prepareAttemptWorkspace(req, workDir)
	if err != nil {
		return ExecuteResult{Error: err.Error()}, nil
	}

	if req.Environment != nil {
		probeContext := req.Context
		if probeContext == nil {
			probeContext = s.Context
		}
		if probeContext == nil {
			probeContext = context.Background()
		}
		report := s.probe(probeContext, req.Executor, func() (sandbox.Launch, error) {
			return s.launch(req, script, workDir, "--muxlio-probe")
		})
		if !report.Ready {
			return ExecuteResult{Error: "pinned environment readiness failed"}, nil
		}
		if err := req.Environment.CheckRunners(report.RunnerVersions()); err != nil {
			return ExecuteResult{Error: err.Error()}, nil
		}
	}

	// Use the shared wire envelope; worker scheduling and policy stay local.
	stdin, err := json.Marshal(contract.Request{
		TraceContext: telemetry.Inject(req.Context),
		Executor:     req.Executor, ExecutorVersion: req.ExecutorVersion, Environment: req.Environment,
		ContractVersion: req.ContractVersion, RequiredCapabilities: req.RequiredCapabilities,
		Action: req.Action, OperationAttemptID: req.OperationAttemptID, Effect: req.Effect, IdempotencyKey: req.IdempotencyKey,
		SessionInputs: req.SessionInputs, Session: req.Session, SessionID: req.SessionID, RemoteTask: req.RemoteTask,
		Workspace: req.Workspace, WorkDir: workDir,
		HostExecutionAllowed: s.Executors[req.Executor].ExplicitlyAllowed(),
		RunID:                req.RunID, ActivationID: req.ActivationID, AttemptID: req.AttemptID,
		Task: req.Task, Params: req.Params, Inputs: req.Inputs, Outputs: req.Outputs,
		Secrets: req.Secrets, ScopePath: req.ScopePath, NodeID: req.NodeID,
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("muxlio: marshalling script input: %w", err)
	}

	launch, err := s.launch(req, script, workDir)
	if err != nil {
		var envErr *executorEnvironmentError
		if errors.As(err, &envErr) {
			return ExecuteResult{}, envErr.Err
		}
		return ExecuteResult{Error: err.Error()}, nil
	}
	if launch.Cleanup != nil {
		defer launch.Cleanup()
	}
	cmd := launch.Command
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if seconds := s.Executors[req.Executor].TimeoutSeconds; seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	parent := req.Context
	if parent == nil {
		parent = s.Context
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	configureProcess(cmd)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Dir = workDir

	logCleanup, err := startAdapterLogPump(cmd, req)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("muxlio: adapter log stream: %w", err)
	}
	defer logCleanup()

	stdout := boundedBuffer{limit: 4 << 20}
	stderr := boundedBuffer{limit: 64 << 10}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := runProcess(ctx, cmd)
	if ctx.Err() != nil {
		return ExecuteResult{Error: "adapter execution timed out or cancelled", Logs: RedactSecrets(stderr.String(), executionSecrets(req))}, nil
	}
	if runErr != nil && stdout.Len() == 0 {
		// Script failed with no stdout — surface stderr as executor error.
		return ExecuteResult{
			Error: fmt.Sprintf("adapter %q failed: %v", req.Executor, runErr), Logs: RedactSecrets(stderr.String(), executionSecrets(req)),
		}, nil
	}

	// Parse stdout as ExecuteResult.
	result, err := contract.ParseResult(stdout.Bytes())
	if err != nil {
		return ExecuteResult{
			Error: fmt.Sprintf("adapter %q returned invalid JSON: %v\nstdout: %s\nstderr: %s",
				req.Executor, err, boundedLog(RedactSecrets(stdout.String(), executionSecrets(req))), RedactSecrets(stderr.String(), executionSecrets(req))), Logs: RedactSecrets(stderr.String(), executionSecrets(req)),
		}, nil
	}
	result.Error = RedactSecrets(result.Error, executionSecrets(req))
	if result.Reconciliation != nil {
		result.Reconciliation.Reason = diagnostic.Redact(RedactSecrets(result.Reconciliation.Reason, executionSecrets(req)))
	}
	if runErr != nil {
		result.Error = fmt.Sprintf("adapter %q failed: %v", req.Executor, runErr)
	}
	result.Logs = boundedLog(RedactSecrets(result.Logs+stderr.String(), executionSecrets(req)))
	if req.WorkspaceMode == "shared-write" {
		result.Logs = "[muxlio shared writable workspace: exclusive resource keys required]\n" + result.Logs
	}
	boundaries, _ := json.Marshal(launch.Capabilities)
	result.Logs = "[muxlio sandbox boundaries: " + string(boundaries) + "]\n" + result.Logs
	result.Logs = boundedLog(result.Logs)
	if result.RemoteTask != nil && result.RemoteTask.Pending {
		// cmd.Run has returned: the local process is fully supervised through
		// exit. Remote output files do not exist until a later completed poll.
		return result, nil
	}

	// Auto-populate any declared output port whose file the script wrote to workDir.
	if result.Outputs == nil {
		result.Outputs = make(map[string]any)
	}
	for port, decl := range req.Outputs {
		if _, ok := result.Outputs[port]; ok {
			// Returned JSON values are authoritative, including objects with data fields.
			continue
		}
		name := decl.Path
		if name == "" {
			name = port
		}
		fullPath := filepath.Join(workDir, name)
		resolved, resolveErr := filepath.EvalSymlinks(fullPath)
		if resolveErr != nil {
			continue
		}
		rel, relErr := filepath.Rel(workDir, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ExecuteResult{Error: "adapter output escapes scratch directory"}, nil
		}
		file, err := os.Open(resolved)
		if err != nil {
			continue // file not present — script may not have produced it
		}
		data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		_ = file.Close()
		if err != nil {
			return ExecuteResult{Error: "cannot read adapter output"}, nil
		}
		if len(data) > 4<<20 {
			return ExecuteResult{Error: "adapter output exceeds 4 MiB limit"}, nil
		}
		// Only pathless ports are JSON results; file artifacts retain their bytes.
		if decl.Path == "" {
			var parsed any
			if err := contract.DecodeJSON(data, &parsed); err == nil {
				result.Outputs[port] = parsed
			} else {
				result.Outputs[port] = string(data)
				if result.OutputErrors == nil {
					result.OutputErrors = make(map[string]string)
				}
				result.OutputErrors[port] = fmt.Sprintf("result port must contain JSON: %v", err)
			}
			continue
		}
		result.Outputs[port] = map[string]any{
			"mime": decl.Mime,
			"path": fullPath,
			"data": string(data),
		}
	}

	return result, nil
}

func safePathPart(value string) string { return runtimefiles.PathPart(value) }

// launch delegates all runtime protocols and boundary descriptions to sandbox.
func (s *ScriptExecutor) launch(req ExecuteRequest, script, workDir string, arguments ...string) (sandbox.Launch, error) {
	if err := ValidatePermissions(req); err != nil {
		return sandbox.Launch{}, err
	}
	var params struct {
		Isolation *isolationPolicy `json:"isolation"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return sandbox.Launch{}, fmt.Errorf("invalid execute params: %w", err)
		}
	}
	policy := isolationPolicy{Mode: "trusted-native"}
	if params.Isolation != nil {
		policy = *params.Isolation
	}
	env, err := s.environment(workDir, policy.Mode == "required", req.Executor)
	if err != nil {
		return sandbox.Launch{}, &executorEnvironmentError{Err: err}
	}
	env = req.Environment.FilterEnvironment(env)
	launch, err := sandbox.Start(sandbox.Config{Script: script, AdapterDir: s.adapterDir, Scratch: workDir,
		Workspace: req.Workspace, WorkspaceMode: req.WorkspaceMode, Environment: env, Arguments: arguments}, policy)
	if err == nil {
		launch.Command.Dir = workDir
	}
	return launch, err
}

// Invalid operator configuration remains a runner error, distinct from a
// request that cannot be satisfied by an isolation backend.
type executorEnvironmentError struct{ Err error }

func (e *executorEnvironmentError) Error() string { return e.Err.Error() }
func (e *executorEnvironmentError) Unwrap() error { return e.Err }

func (s *ScriptExecutor) command(req ExecuteRequest, script, workDir string) (*exec.Cmd, bool, error) {
	launch, err := s.launch(req, script, workDir)
	return launch.Command, launch.Capabilities.Isolated(), err
}

func RedactSecrets(value string, secrets map[string]string) string {
	values := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		values = append(values, secret)
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, secret := range values {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
			// A bounded stream may end halfway through a secret.
			for n := len(secret) - 1; n > 0; n-- {
				if strings.HasSuffix(value, secret[:n]) {
					value = strings.TrimSuffix(value, secret[:n]) + "[REDACTED]"
					break
				}
			}
		}
	}
	return value
}

const MaxLogBytes = 64 << 10

func boundedLog(s string) string {
	if len(s) > MaxLogBytes {
		return s[:MaxLogBytes] + "\n[truncated]"
	}
	return s
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func (s *ScriptExecutor) environment(workDir string, isolated bool, executor string) ([]string, error) {
	home := workDir
	if s.AgentHome != "" {
		if !filepath.IsAbs(s.AgentHome) {
			return nil, fmt.Errorf("agent home must be an absolute existing directory")
		}
		info, homeErr := os.Stat(s.AgentHome)
		if homeErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("agent home must be an absolute existing directory")
		}
		if !isolated {
			home = s.AgentHome
		}
	} else if !isolated {
		// Native processes use the operator's home unless explicitly configured.
		// Isolated processes retain the scratch HOME.
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			home = h
		}
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + workDir, "LANG=C.UTF-8"}
	if !isolated {
		if err := ValidateWorkerEnv(s.WorkerEnv); err != nil {
			return nil, fmt.Errorf("workerEnv: %w", err)
		}
		config := s.Executors[executor]
		if err := ValidateExecutorConfigs(map[string]ExecutorConfig{executor: config}); err != nil {
			return nil, err
		}
		merged := make(map[string]string, len(s.WorkerEnv)+len(config.Env))
		for key, value := range s.WorkerEnv {
			merged[key] = value
		}
		for key, value := range config.Env {
			merged[key] = value
		}
		for key, value := range merged {
			prefix := key + "="
			for i := 0; i < len(env); i++ {
				if strings.HasPrefix(env[i], prefix) {
					env = append(env[:i], env[i+1:]...)
					break
				}
			}
			env = append(env, prefix+value)
		}
	}
	return env, nil
}
