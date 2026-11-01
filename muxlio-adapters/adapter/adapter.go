// Package adapter defines the JSON protocol between muxlio's ScriptExecutor
// and adapter binaries. Every adapter reads a Request from stdin and writes
// a Response to stdout.
package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OutputDecl mirrors workers.OutputDecl.
type OutputDecl struct {
	Mime string `json:"mime,omitempty"`
	Path string `json:"path,omitempty"`
}

// Request is what ScriptExecutor sends on stdin: the standard ExecuteRequest
// fields plus a WorkDir for this invocation's scratch space.
type Request struct {
	Workspace    string                `json:"workspace"`
	RunID        string                `json:"runId,omitempty"`
	ActivationID string                `json:"activationId,omitempty"`
	AttemptID    string                `json:"attemptId,omitempty"`
	Executor     string                `json:"executor"`
	Task         string                `json:"task,omitempty"`
	Params       json.RawMessage       `json:"params,omitempty"`
	Inputs       map[string]any        `json:"inputs,omitempty"`
	Outputs      map[string]OutputDecl `json:"outputs,omitempty"`
	ScopePath    []string              `json:"scopePath,omitempty"`
	NodeID       string                `json:"nodeId,omitempty"`
	WorkDir      string                `json:"workDir"`
}

// Response is what the adapter writes to stdout.
type Response struct {
	Outputs map[string]any `json:"outputs"`
	Error   string         `json:"error"`
}

// ReadRequest decodes the Request from stdin.
func ReadRequest() (*Request, error) {
	var req Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		return nil, fmt.Errorf("adapter: reading request: %w", err)
	}
	return &req, nil
}

// WriteResponse encodes the Response to stdout.
func WriteResponse(resp Response) error {
	return json.NewEncoder(os.Stdout).Encode(resp)
}

// Fail writes an error Response and exits 0 (adapter errors are
// surfaced via Response.Error, not the process exit code).
func Fail(format string, args ...any) {
	_ = WriteResponse(Response{Outputs: map[string]any{}, Error: fmt.Sprintf(format, args...)})
	os.Exit(0)
}

// InputString extracts a plain string value from an input port. The port
// value may be a raw string, or a map with a "data" key.
func InputString(inputs map[string]any, port string) string {
	v, ok := inputs[port]
	if !ok {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if d, ok := val["data"].(string); ok {
			return d
		}
	}
	// Fall back to JSON encoding so downstream can still read it.
	b, _ := json.Marshal(v)
	return string(b)
}

// CleanEnvForChild returns the current process environment with muxlio-internal
// variables stripped out. Adapters must call this when building exec.Cmd.Env
// for any child CLI process they spawn (cursor, kiro, claude, gemini, etc.).
//
// Background: the script executor injects MUXLIO_LOG_FD=3 into the adapter's
// environment and opens fd 3 as a pipe write-end for live log streaming. If
// that variable (or the open fd) leaks into a child CLI process, some CLIs
// (notably cursor-agent) detect the writable fd 3 and redirect their own
// output there instead of stdout, causing the adapter's StdoutPipe scanner to
// see an empty stream and fail with "stream ended without a completion event".
func CleanEnvForChild() []string {
	env := os.Environ()
	out := env[:0:len(env)]
	for _, kv := range env {
		if strings.HasPrefix(kv, "MUXLIO_LOG_FD=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func WorkspaceRoot(req *Request) (string, error) {
	if req.Workspace == "" || !filepath.IsAbs(req.Workspace) {
		return "", fmt.Errorf("explicit absolute workspace is required")
	}
	root, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace must be a directory")
	}
	return root, nil
}
