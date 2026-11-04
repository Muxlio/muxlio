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
	HostExecutionAllowed bool                  `json:"hostExecutionAllowed,omitempty"`
	Workspace            string                `json:"workspace"`
	RunID                string                `json:"runId,omitempty"`
	ActivationID         string                `json:"activationId,omitempty"`
	AttemptID            string                `json:"attemptId,omitempty"`
	Executor             string                `json:"executor"`
	Task                 string                `json:"task,omitempty"`
	Params               json.RawMessage       `json:"params,omitempty"`
	Inputs               map[string]any        `json:"inputs,omitempty"`
	Outputs              map[string]OutputDecl `json:"outputs,omitempty"`
	ScopePath            []string              `json:"scopePath,omitempty"`
	NodeID               string                `json:"nodeId,omitempty"`
	WorkDir              string                `json:"workDir"`
}

// Response is what the adapter writes to stdout.
type Response struct {
	Outputs map[string]any `json:"outputs"`
	Error   string         `json:"error"`
	Usage   *TokenUsage    `json:"usage,omitempty"`
}

// TokenUsage holds provider-reported token counts. A nil pointer on Response
// means the provider did not supply usage; a non-nil zero value is reported zero.
type TokenUsage struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
	Cached int64 `json:"cached"`
}

// ParseTokenUsage parses common provider usage shapes while preserving absence.
// Input keys: input_tokens, prompt_token_count, prompt_tokens, uncachedInputTokens, inputTokens,
// promptTokenCount.
// Output keys: output_tokens, candidates_token_count, completion_tokens,
// outputTokens, candidatesTokenCount.
// Cached keys: cached_input_tokens, cache_read_input_tokens, cacheReadInputTokens, cacheReadTokens,
// cached_content_token_count, cached_tokens, cachedTokens,
// cachedContentTokenCount, cached (Gemini result.stats).
//
// The camelCase aliases cover the Gemini CLI's stream-json "usage" event,
// which surfaces provider-reported counts at the top level of the event
// object using camelCase field names (inputTokens, outputTokens,
// cachedTokens) and the raw Gemini usageMetadata names (promptTokenCount,
// candidatesTokenCount, cachedContentTokenCount).
func ParseTokenUsage(raw json.RawMessage) *TokenUsage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	var u TokenUsage
	var found bool
	read := func(keys ...string) int64 {
		for _, key := range keys {
			if b, ok := values[key]; ok {
				var n *int64
				if json.Unmarshal(b, &n) == nil && n != nil && *n >= 0 {
					found = true
					return *n
				}
			}
		}
		return 0
	}
	u.Input = read("input_tokens", "prompt_token_count", "prompt_tokens", "uncachedInputTokens", "inputTokens", "promptTokenCount")
	u.Output = read("output_tokens", "candidates_token_count", "completion_tokens", "outputTokens", "candidatesTokenCount")
	u.Cached = read("cached_input_tokens", "cache_read_input_tokens", "cacheReadInputTokens", "cacheReadTokens", "cached_content_token_count", "cached_tokens", "cachedTokens", "cachedContentTokenCount", "cached")
	if !found {
		return nil
	}
	return &u
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

// FailWithUsage writes an error Response that also carries provider-reported
// usage, then exits 0. Use it when a terminal result event reported usage even
// though the run failed, so a failed attempt keeps its real token counts
// instead of being recorded as unavailable. A nil usage is reported as absent
// (omitted via omitempty), matching Fail.
func FailWithUsage(usage *TokenUsage, format string, args ...any) {
	_ = WriteResponse(Response{Outputs: map[string]any{}, Error: fmt.Sprintf(format, args...), Usage: usage})
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
