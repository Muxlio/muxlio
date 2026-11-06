// command is Muxlio's language-neutral direct process executor. It deliberately
// does not invoke a shell; program and args are passed to os/exec as argv.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/muxlio/muxlio-adapters/adapter"
)

const previewLimit = 64 << 10

var logWriteMu sync.Mutex

type params struct {
	Program      string            `json:"program"`
	Args         []string          `json:"args"`
	ArgTemplates []*string         `json:"argTemplates"`
	Cwd          string            `json:"cwd"`
	Env          map[string]string `json:"env"`
	Stdin        string            `json:"stdin"`
	Timeout      string            `json:"timeout"`
	OnNonZero    string            `json:"onNonZero"`
	FailOnOutput bool              `json:"failOnOutput"`
	TrimStdout   bool              `json:"trimStdout"`
	Execution    struct {
		Isolation string `json:"isolation"`
	} `json:"execution"`
}
type preview struct {
	b strings.Builder
	n int
}

func (p *preview) Write(b []byte) (int, error) {
	n := len(b)
	if p.n < previewLimit {
		k := previewLimit - p.n
		if k > len(b) {
			k = len(b)
		}
		p.b.Write(b[:k])
		p.n += k
	}
	return n, nil
}
func (p *preview) String() string {
	s := p.b.String()
	if p.n >= previewLimit {
		return s + "\n[preview truncated at 65536 bytes]"
	}
	return s
}

func main() {
	if adapter.ProbeRequested(os.Args) {
		adapter.RunProbe(contract.ProbeCheck{Name: contract.ProbeVersion, Passed: true, Message: "direct process runner version 1"}, contract.ProbeCheck{Name: contract.ProbeBackend, Passed: true, Message: "host process execution; sandbox backend unavailable"})
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("command: %v", err)
	}
	var p params
	if len(req.Params) > 0 {
		if err = json.Unmarshal(req.Params, &p); err != nil {
			adapter.Fail("command: invalid params: %v", err)
		}
	}
	if p.Execution.Isolation == "sandbox" {
		adapter.Fail("command sandbox requested but no verified OCI backend is configured")
	}
	if p.Execution.Isolation != "" && p.Execution.Isolation != "host" {
		adapter.Fail("command execution.isolation must be host or sandbox")
	}
	if !req.HostExecutionAllowed {
		adapter.Fail("command host execution denied by worker policy")
	}
	if p.Program == "" || strings.IndexByte(p.Program, 0) >= 0 {
		adapter.Fail("command: program must be a nonempty executable")
	}
	if len(p.Program) > 4096 || len(p.Args) > 4096 {
		adapter.Fail("command: program/argument count exceeds limits")
	}
	for _, a := range p.Args {
		if strings.IndexByte(a, 0) >= 0 || len(a) > 1<<20 {
			adapter.Fail("command: invalid or oversized argument")
		}
	}
	if len(p.ArgTemplates) > 0 {
		if len(p.ArgTemplates) != len(p.Args) {
			adapter.Fail("command: argTemplates must match args length")
		}
		for i, t := range p.ArgTemplates {
			if t != nil {
				value, e := renderArgTemplate(*t, req.Inputs)
				if e != nil {
					adapter.Fail("command: argument template: %v", e)
				}
				p.Args[i] = value
			}
		}
	}
	if p.OnNonZero == "" {
		p.OnNonZero = "fail"
	}
	if p.OnNonZero != "fail" && p.OnNonZero != "return" {
		adapter.Fail("command: onNonZero must be fail or return")
	}
	root, err := adapter.WorkspaceRoot(req)
	if err != nil {
		adapter.Fail("command: %v", err)
	}
	cwd := root
	if p.Cwd != "" && p.Cwd != "." {
		if filepath.IsAbs(p.Cwd) {
			adapter.Fail("command: cwd must be relative to workspace")
		}
		cwd = filepath.Join(root, filepath.Clean(p.Cwd))
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		adapter.Fail("command: resolving cwd: %v", err)
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		adapter.Fail("command: cwd escapes workspace")
	}
	if st, e := os.Stat(cwd); e != nil || !st.IsDir() {
		adapter.Fail("command: cwd is not a directory")
	}
	if p.Timeout == "" {
		p.Timeout = "10m"
	}
	timeout, err := time.ParseDuration(p.Timeout)
	if err != nil || timeout <= 0 || timeout > 10*time.Minute {
		adapter.Fail("command: timeout must be positive and at most 10m")
	}
	if req.WorkDir == "" {
		adapter.Fail("command: artifact directory missing")
	}
	for _, d := range req.Outputs {
		if d.Path != "" {
			if e := adapter.ValidateOutputPath(req.WorkDir, d.Path); e != nil {
				adapter.Fail("command: invalid declared output path: %v", e)
			}
		}
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	workerPath := os.Getenv("PATH")
	if workerPath == "" {
		workerPath = "/usr/local/bin:/usr/bin:/bin"
	}
	program, err := resolveProgram(p.Program, cwd, root, workerPath)
	if err != nil {
		adapter.Fail("command: %v", err)
	}
	cmd := exec.Command(program, p.Args...)
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=" + workerPath, "HOME=" + req.WorkDir, "TMPDIR=" + req.WorkDir, "LANG=C.UTF-8", "MUXLIO_OUTPUT_DIR=" + req.WorkDir}
	// The worker supplies a minimal environment plus this executor's configured
	// values. Forward those values generically, retaining command scratch paths.
	managedEnv := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "MUXLIO_OUTPUT_DIR": true}
	for _, entry := range adapter.CleanEnvForChild() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !managedEnv[key] {
			if _, overridden := p.Env[key]; !overridden {
				cmd.Env = append(cmd.Env, entry)
			}
		}
	}
	for k, v := range p.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.IndexByte(v, 0) >= 0 {
			adapter.Fail("command: invalid environment entry")
		}
		if managedEnv[k] {
			adapter.Fail("command: environment key %q is worker-managed", k)
		}
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(p.Stdin)
	var out, stderr preview
	cmd.Stdout = io.MultiWriter(&out, streamWriter{"stdout"})
	cmd.Stderr = io.MultiWriter(&stderr, streamWriter{"stderr"})
	started := time.Now()
	runErr := runCommand(ctx, cmd)
	duration := time.Since(started).Milliseconds()
	if parent.Err() != nil {
		adapter.Fail("command: cancelled")
	}
	if ctx.Err() != nil {
		adapter.Fail("command: timed out after %s", timeout)
	}
	result := map[string]any{"exitCode": nil, "signal": nil, "stdout": out.String(), "stderr": stderr.String(), "durationMs": duration}
	if runErr == nil {
		result["exitCode"] = 0
	} else if ee, ok := runErr.(*exec.ExitError); ok {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result["exitCode"] = nil
			result["signal"] = status.Signal().String()
		} else {
			result["exitCode"] = ee.ExitCode()
		}
	} else {
		adapter.Fail("command: failed to start %q: %v", p.Program, runErr)
	}
	passed := result["exitCode"] == 0
	result["passed"] = passed
	if p.TrimStdout {
		result["stdout"] = strings.TrimSpace(out.String())
	}
	if p.FailOnOutput && out.String() != "" {
		_ = adapter.WriteResponse(adapter.Response{Outputs: result, Error: "command produced output while failOnOutput is enabled"})
		return
	}
	// Preserve onNonZero=return for callers inspecting process failures.
	if sig, ok := result["signal"].(string); ok && sig != "" && p.OnNonZero == "fail" {
		_ = adapter.WriteResponse(adapter.Response{Outputs: result, Error: fmt.Sprintf("command terminated by signal: %s", sig)})
		return
	}
	if code, ok := result["exitCode"].(int); ok && code != 0 && p.OnNonZero == "fail" {
		_ = adapter.WriteResponse(adapter.Response{Outputs: result, Error: fmt.Sprintf("command exited with status %d", code)})
		return
	}
	declared := *req
	declared.Outputs = make(map[string]adapter.OutputDecl)
	for port, decl := range req.Outputs {
		if _, builtin := result[port]; !builtin || decl.Path != "" {
			declared.Outputs[port] = decl
		}
	}
	outputs, missing, outputErrors := adapter.CollectOutputs(&declared)
	for port, value := range outputs {
		result[port] = value
	}
	if len(missing) > 0 {
		_ = adapter.WriteResponse(adapter.Response{Outputs: result, OutputErrors: outputErrors, Error: fmt.Sprintf("command did not write expected output files: %s", strings.Join(missing, ", "))})
		return
	}
	// Keep full output available through normal Muxlio task logs; the adapter
	// response holds only the bounded previews.
	_ = adapter.WriteResponse(adapter.Response{Outputs: result, OutputErrors: outputErrors})
}

var argPlaceholder = regexp.MustCompile(`\{\{([A-Za-z0-9_.-]+)\}\}`)

func renderArgTemplate(template string, inputs map[string]any) (string, error) {
	var renderErr error
	out := argPlaceholder.ReplaceAllStringFunc(template, func(token string) string {
		path := argPlaceholder.FindStringSubmatch(token)[1]
		parts := strings.Split(path, ".")
		var value any = inputs
		for _, part := range parts {
			obj, ok := value.(map[string]any)
			if !ok {
				renderErr = fmt.Errorf("input path %q was not found", path)
				return ""
			}
			value, ok = obj[part]
			if !ok {
				renderErr = fmt.Errorf("input path %q was not found", path)
				return ""
			}
		}
		switch v := value.(type) {
		case string:
			return v
		case float64, bool:
			return fmt.Sprint(v)
		default:
			renderErr = fmt.Errorf("input path %q is not a scalar", path)
			return ""
		}
	})
	if renderErr != nil {
		return "", renderErr
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		return "", fmt.Errorf("malformed placeholder")
	}
	return out, nil
}

type streamWriter struct{ name string }

func (w streamWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		size := len(b)
		if size > 8<<10 {
			size = 8 << 10
		}
		logWriteMu.Lock()
		adapter.AppendLogChunk(w.name, string(b[:size]))
		logWriteMu.Unlock()
		b = b[size:]
	}
	return n, nil
}

func resolveProgram(program, cwd, workspace, searchPath string) (string, error) {
	if filepath.IsAbs(program) {
		return executableFile(program)
	}
	if strings.ContainsAny(program, `/\\`) {
		candidate := filepath.Join(cwd, program)
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", fmt.Errorf("executable %q not found: %w", program, err)
		}
		rel, err := filepath.Rel(workspace, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("executable path escapes workspace")
		}
		return executableFile(resolved)
	}
	for _, dir := range filepath.SplitList(searchPath) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, program)
		if resolved, err := executableFile(candidate); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("executable %q not found in worker-configured PATH", program)
}

func executableFile(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("executable %q is not an executable regular file", path)
	}
	return resolved, nil
}
