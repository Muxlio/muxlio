package workers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
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

// isolationPolicy is read from the execute step's params.isolation object.
type isolationPolicy struct {
	Mode    string `json:"mode"`              // required or trusted-native
	Runtime string `json:"runtime,omitempty"` // bwrap, oci, or macos-vm
	Image   string `json:"image,omitempty"`   // required for oci
	Enabled bool   `json:"enabled,omitempty"` // explicit opt-in for optional drivers
}

// isolationDriver describes a platform isolation backend. A backend is only
// considered usable when it can build an enforced command for the request.
type isolationDriver interface {
	Compatibility() error
	Command(*ScriptExecutor, string, string, isolationPolicy) (*exec.Cmd, error)
}

// macOSVMDriver is an extension point for a future Virtualization.framework
// implementation. Compatibility checks intentionally do not imply that VM
// execution is available or secure.
type macOSVMDriver struct {
	goos string
}

func (d macOSVMDriver) Compatibility() error {
	if d.goos != "darwin" {
		return fmt.Errorf("macOS VM isolation requires macOS")
	}
	return nil
}

func (d macOSVMDriver) Command(_ *ScriptExecutor, _, _ string, _ isolationPolicy) (*exec.Cmd, error) {
	if err := d.Compatibility(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("macOS VM isolation is not implemented; no secure VM driver is available")
}

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
func (s *ScriptExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	started := time.Now()
	req, resolveErr := s.resolveSecrets(req)
	if resolveErr != nil {
		return ExecuteResult{Error: resolveErr.Error()}, nil
	}
	result, err := s.executeSession(req)
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
		d.Requirements = map[string]any{"contractVersion": req.ContractVersion, "executorVersion": req.ExecutorVersion, "capabilities": req.RequiredCapabilities, "sessionRequested": req.SessionID != "", "remoteTaskRequested": req.RemoteTask != nil}
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

	// Use the shared wire envelope; worker scheduling and policy stay local.
	stdin, err := json.Marshal(contract.Request{
		Executor: req.Executor, ExecutorVersion: req.ExecutorVersion,
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

	cmd, isolated, err := s.command(req, script, workDir)
	if err != nil {
		return ExecuteResult{Error: err.Error()}, nil
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
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
	cmd.Env, err = s.environment(workDir, isolated, req.Executor)
	if err != nil {
		return ExecuteResult{}, err
	}
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
	if isolated {
		result.Logs = "[muxlio isolation enforced; network=none; filesystem=scratch plus declared workspace mounts]\n" + result.Logs
	} else {
		result.Logs = "[muxlio trusted native execution; network=host; filesystem=host; agent tool calls are not intercepted]\n" + result.Logs
	}
	result.Logs = boundedLog(result.Logs)

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
			if err := json.Unmarshal(data, &parsed); err == nil {
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

func safePathPart(value string) string {
	var cleaned strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			cleaned.WriteRune(r)
		}
	}
	if cleaned.String() != value {
		return fmt.Sprintf("~%x", sha256.Sum256([]byte(value)))
	}
	value = cleaned.String()
	if value == "" {
		return "unknown"
	}
	return value
}

func (s *ScriptExecutor) command(req ExecuteRequest, script, workDir string) (*exec.Cmd, bool, error) {
	if err := ValidatePermissions(req); err != nil {
		return nil, false, err
	}
	var params struct {
		Isolation *isolationPolicy `json:"isolation"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, false, fmt.Errorf("invalid execute params: %w", err)
		}
	}
	policy := params.Isolation
	if policy == nil {
		return exec.Command(script), false, nil
	}
	switch policy.Mode {
	case "trusted-native":
		return exec.Command(script), false, nil
	case "required":
	default:
		return nil, false, fmt.Errorf("isolation mode must be required or trusted-native")
	}
	switch policy.Runtime {
	case "macos-vm":
		if !policy.Enabled {
			return nil, false, fmt.Errorf("macOS VM isolation requires params.isolation.enabled=true")
		}
		driver := macOSVMDriver{goos: runtime.GOOS}
		cmd, err := driver.Command(s, script, workDir, *policy)
		return cmd, false, err
	case "bwrap":
		if runtime.GOOS != "linux" {
			return nil, false, fmt.Errorf("required bwrap isolation unavailable: Linux is required")
		}
		bwrap, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, false, fmt.Errorf("required bwrap isolation unavailable: %w", err)
		}
		// Keep the host root out of the namespace. Binding / wholesale makes
		// host credentials and unrelated user data readable by the adapter.
		args := []string{"--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-net", "--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin", "--ro-bind", "/lib", "/lib", "--ro-bind", "/lib64", "/lib64", "--dev", "/dev", "--proc", "/proc", "--ro-bind", s.adapterDir, "/muxlio/adapters", "--bind", workDir, workDir, "--chdir", workDir, "/muxlio/adapters/" + filepath.Base(script)}
		if req.Workspace != "" && req.WorkspaceMode != "snapshot" && req.WorkspaceMode != "" {
			flag := "--ro-bind"
			if req.WorkspaceMode == "shared-write" {
				flag = "--bind"
			}
			args = append(args[:len(args)-1], flag, req.Workspace, req.Workspace, args[len(args)-1])
		}
		return exec.Command(bwrap, args...), true, nil
	case "oci":
		if runtime.GOOS != "linux" {
			return nil, false, fmt.Errorf("required OCI isolation unavailable: Linux is required")
		}
		if policy.Image == "" {
			return nil, false, fmt.Errorf("required OCI isolation needs params.isolation.image")
		}
		engine, err := exec.LookPath("podman")
		if err != nil {
			engine, err = exec.LookPath("docker")
		}
		if err != nil {
			return nil, false, fmt.Errorf("required OCI isolation unavailable: podman or docker is required")
		}
		adapterMount := s.adapterDir + ":/muxlio/adapters:ro"
		workMount := workDir + ":" + workDir + ":rw"
		args := []string{"run", "--rm", "-i", "--network=none", "--read-only", "--tmpfs", "/tmp", "-v", adapterMount, "-v", workMount, "-w", workDir}
		if filepath.Base(engine) == "podman" {
			args = append(args, "--userns=keep-id")
		} else {
			// Docker does not implement Podman's keep-id value. Use the host
			// uid/gid explicitly so the writable work mount is usable without
			// running the adapter as root in the container.
			args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
		}
		if req.Workspace != "" && req.WorkspaceMode != "snapshot" && req.WorkspaceMode != "" {
			mode := "ro"
			if req.WorkspaceMode == "shared-write" {
				mode = "rw"
			}
			args = append(args, "-v", req.Workspace+":"+req.Workspace+":"+mode)
		}
		args = append(args, policy.Image, "/muxlio/adapters/"+filepath.Base(script))
		return exec.Command(engine, args...), true, nil
	default:
		return nil, false, fmt.Errorf("required isolation runtime must be bwrap, oci, or macos-vm")
	}
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
