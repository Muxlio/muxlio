package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ScriptExecutor dispatches execute requests to per-executor shell scripts.
//
// The executor name (e.g. "agent/codex") is mapped to a script filename by
// replacing "/" with "-":
//
//	agent/claude      → <adapterDir>/agent-claude
//	agent/codex       → <adapterDir>/agent-codex
//	agent/gemini-cli  → <adapterDir>/agent-gemini-cli
//	command/go-test   → <adapterDir>/command-go-test
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
	adapterDir string
	workBase   string
	Workspace  string
	// AgentHome explicitly opts native adapters into a dedicated provider login
	// directory. Isolated adapters always keep their scratch HOME.
	AgentHome string
	Timeout   time.Duration
	Context   context.Context
	// AllowCommandHost is an administrator-controlled worker setting. Workflow
	// parameters never enable this capability.
	AllowCommandHost bool
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
	return &ScriptExecutor{adapterDir: adapterDir, workBase: workBase}
}

// scriptPath converts an executor name to an absolute script path.
func (s *ScriptExecutor) scriptPath(executor string) string {
	name := strings.ReplaceAll(executor, "/", "-")
	return filepath.Join(s.adapterDir, name)
}

// Execute implements Executor.
func (s *ScriptExecutor) Execute(req ExecuteRequest) (ExecuteResult, error) {
	req.Workspace = ignoreMissingWorkspace(req.Workspace)
	if ws := workspaceFromExecuteInputs(req.Inputs); ws != "" {
		req.Workspace = ignoreMissingWorkspace(ws)
	} else if s.Workspace != "" {
		req.Workspace = s.Workspace
	}
	var guardErr error
	req, guardErr = GuardRequest(req)
	if guardErr != nil {
		return ExecuteResult{Error: guardErr.Error()}, nil
	}
	if req.Executor == "command" {
		var params struct {
			Execution struct {
				Isolation string `json:"isolation"`
			} `json:"execution"`
		}
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		mode := params.Execution.Isolation
		if mode == "sandbox" {
			return ExecuteResult{Error: "command sandbox requested but no verified OCI backend is configured"}, nil
		}
		if mode != "" && mode != "host" {
			return ExecuteResult{Error: "command execution.isolation must be host or sandbox"}, nil
		}
		if !s.AllowCommandHost {
			return ExecuteResult{Error: "command host execution denied by worker policy"}, nil
		}
	}
	if strings.Contains(req.Executor, "..") || strings.ContainsAny(req.Executor, "\\") {
		return ExecuteResult{}, fmt.Errorf("invalid executor name")
	}
	script := s.scriptPath(req.Executor)
	if _, err := os.Stat(script); err != nil {
		return ExecuteResult{}, fmt.Errorf(
			"muxlio: no adapter for executor %q (expected script at %s)", req.Executor, script)
	}

	// Keep each attempt's workspace under the run so incidental agent files
	// (notes, scratch output, CLI diagnostics) cannot land in the source repo.
	// Older/direct callers without execution IDs retain an isolated temp dir.
	var workDir string
	var err error
	if req.RunID != "" && req.ActivationID != "" && req.AttemptID != "" {
		workDir = filepath.Join(s.workBase, "..", "runs", safePathPart(req.RunID), safePathPart(req.ActivationID), safePathPart(req.AttemptID))
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return ExecuteResult{}, fmt.Errorf("muxlio: creating run workspace: %w", err)
		}
	} else {
		if err := os.MkdirAll(s.workBase, 0o755); err != nil {
			return ExecuteResult{}, fmt.Errorf("muxlio: creating work base: %w", err)
		}
		workDir, err = os.MkdirTemp(s.workBase, "exec-")
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("muxlio: creating work dir: %w", err)
		}
	}
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("muxlio: resolving work dir: %w", err)
	}

	// Build the stdin payload: ExecuteRequest + workDir.
	type scriptInput struct {
		ExecuteRequest
		WorkDir string `json:"workDir"`
	}
	stdin, err := json.Marshal(scriptInput{ExecuteRequest: req, WorkDir: workDir})
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
		return ExecuteResult{Error: "adapter execution timed out or cancelled", Logs: RedactSecrets(stderr.String(), req.Secrets)}, nil
	}
	if runErr != nil && stdout.Len() == 0 {
		// Script failed with no stdout — surface stderr as executor error.
		return ExecuteResult{
			Error: fmt.Sprintf("adapter %q failed: %v", req.Executor, runErr), Logs: RedactSecrets(stderr.String(), req.Secrets),
		}, nil
	}

	// Parse stdout as ExecuteResult.
	var result ExecuteResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return ExecuteResult{
			Error: fmt.Sprintf("adapter %q returned invalid JSON: %v\nstdout: %s\nstderr: %s",
				req.Executor, err, boundedLog(RedactSecrets(stdout.String(), req.Secrets)), RedactSecrets(stderr.String(), req.Secrets)), Logs: RedactSecrets(stderr.String(), req.Secrets),
		}, nil
	}
	result.Error = RedactSecrets(result.Error, req.Secrets)
	if runErr != nil {
		result.Error = fmt.Sprintf("adapter %q failed: %v", req.Executor, runErr)
	}
	result.Logs = boundedLog(RedactSecrets(result.Logs+stderr.String(), req.Secrets))
	if isolated {
		result.Logs = "[muxlio isolation enforced]\n" + result.Logs
	}

	// Auto-populate any declared output port whose file the script wrote to workDir.
	if result.Outputs == nil {
		result.Outputs = make(map[string]any)
	}
	for port, decl := range req.Outputs {
		if _, ok := result.Outputs[port]; ok {
			// Script already set it — but if it's a { mime, path, data } map
			// and the mime is application/json, unwrap data to a parsed value.
			if m, ok := result.Outputs[port].(map[string]any); ok {
				if data, ok := m["data"].(string); ok && isJSONMime(decl.Mime) {
					var parsed any
					if err := json.Unmarshal([]byte(data), &parsed); err == nil {
						result.Outputs[port] = parsed
					}
				}
			}
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
		// For application/json, parse and return the native value directly
		// so expression-language field ops work against it.
		if isJSONMime(decl.Mime) {
			var parsed any
			if err := json.Unmarshal(data, &parsed); err == nil {
				result.Outputs[port] = parsed
				continue
			}
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
	value = cleaned.String()
	if value == "" {
		return "unknown"
	}
	return value
}

func (s *ScriptExecutor) command(req ExecuteRequest, script, workDir string) (*exec.Cmd, bool, error) {
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
		workMount := workDir + ":/work:rw"
		args := []string{"run", "--rm", "--network=none", "--read-only", "--tmpfs", "/tmp", "-v", adapterMount, "-v", workMount, "-w", "/work"}
		if filepath.Base(engine) == "podman" {
			args = append(args, "--userns=keep-id")
		} else {
			// Docker does not implement Podman's keep-id value. Use the host
			// uid/gid explicitly so the writable work mount is usable without
			// running the adapter as root in the container.
			args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
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

// isJSONMime returns true for MIME types whose content should be parsed as JSON.
func isJSONMime(mime string) bool {
	return mime == "application/json" ||
		strings.HasSuffix(mime, "+json") ||
		strings.HasPrefix(mime, "application/json;")
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
		// No explicit agentHome is configured: default native adapters to the
		// operator's real home so they reuse existing provider logins already
		// present there (e.g. ~/.codex, ~/.cursor) instead of a throwaway
		// scratch HOME. Isolated runtimes keep the scratch HOME below.
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			home = h
		}
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + workDir, "LANG=C.UTF-8"}
	// Kiro keeps sign-in state under KIRO_HOME rather than HOME. Forward only
	// this explicit opt-in path for trusted-native adapters; isolated runtimes
	// must not inherit host credentials.
	if !isolated && os.Getenv("KIRO_HOME") != "" {
		env = append(env, "KIRO_HOME="+os.Getenv("KIRO_HOME"))
	}
	// Cursor's agent CLI defaults to the macOS login keychain, which headless
	// SSH and launchd sessions never unlock. Use on-disk credentials instead;
	// the operator runs `AGENT_CLI_CREDENTIAL_STORE=file agent login` once.
	if executor == "agent/cursor" {
		env = append(env, "AGENT_CLI_CREDENTIAL_STORE=file")
	}
	return env, nil
}
