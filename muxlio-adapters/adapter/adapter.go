// Package adapter defines the JSON protocol between muxlio's ScriptExecutor
// and adapter binaries. Every adapter reads a Request from stdin and writes
// a Response to stdout.
package adapter

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

// OutputDecl mirrors workers.OutputDecl.
type OutputDecl = contract.OutputDecl

// Request is what ScriptExecutor sends on stdin: the standard ExecuteRequest
// fields plus a WorkDir for this invocation's scratch space.
type Request = contract.Request

// Response is what the adapter writes to stdout.
type Response = contract.Result

// TokenUsage holds provider-reported token counts. A nil pointer on Response
// means the provider did not supply usage; a non-nil zero value is reported zero.
type TokenUsage = contract.TokenUsage

// ParseTokenUsage parses common provider usage shapes while preserving absence.
// Input keys: input_tokens, prompt_token_count, prompt_tokens, uncachedInputTokens, inputTokens,
// promptTokenCount.
// Output keys: output_tokens, candidates_token_count, completion_tokens,
// outputTokens, candidatesTokenCount.
// Cached keys: cached_input_tokens, cache_read_input_tokens, cacheReadInputTokens, cacheReadTokens,
// cached_content_token_count, cached_tokens, cachedTokens,
// cachedContentTokenCount, cached (Antigravity result.stats).
//
// The camelCase aliases cover the Antigravity CLI stream-json "usage" event,
// which surfaces provider-reported counts at the top level of the event
// object using camelCase field names (inputTokens, outputTokens,
// cachedTokens) and the raw usageMetadata names (promptTokenCount,
// candidatesTokenCount, cachedContentTokenCount).
var parseTokenUsageKeys = []string{
	"input_tokens", "prompt_token_count", "prompt_tokens", "uncachedInputTokens", "inputTokens", "promptTokenCount",
	"output_tokens", "candidates_token_count", "completion_tokens", "outputTokens", "candidatesTokenCount",
	"cached_input_tokens", "cache_read_input_tokens", "cache_read_tokens", "cacheReadInputTokens", "cacheReadTokens",
	"cached_content_token_count", "cached_tokens", "cachedTokens", "cachedContentTokenCount", "cached",
}

func ParseTokenUsage(raw json.RawMessage) *TokenUsage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	for _, key := range parseTokenUsageKeys {
		b, ok := values[key]
		if !ok {
			continue
		}
		var n *int64
		if json.Unmarshal(b, &n) != nil || n == nil || *n < 0 {
			return nil
		}
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
	u.Cached = read("cached_input_tokens", "cache_read_input_tokens", "cache_read_tokens", "cacheReadInputTokens", "cacheReadTokens", "cached_content_token_count", "cached_tokens", "cachedTokens", "cachedContentTokenCount", "cached")
	if !found {
		return nil
	}
	return &u
}

var responseSecrets map[string]string

// ReadRequest decodes the Request from stdin.
func ReadRequest() (*Request, error) {
	var req *Request
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		return nil, fmt.Errorf("adapter: reading request: %w", err)
	}
	if req == nil {
		return nil, fmt.Errorf("adapter: request must be an object")
	}
	responseSecrets = req.Secrets
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("adapter: request must contain exactly one JSON value")
	}
	return req, nil
}

// WriteResponse encodes the Response to stdout.
func WriteResponse(resp Response) error {
	// Redact diagnostics before JSON encoding, so escaped secrets are covered.
	resp.Error = redact(resp.Error)
	resp.Logs = redact(resp.Logs)
	for port, message := range resp.OutputErrors {
		resp.OutputErrors[port] = redact(message)
	}
	for _, port := range []string{"stdout", "stderr", "_text"} {
		if value, ok := resp.Outputs[port].(string); ok {
			resp.Outputs[port] = redact(value)
		}
		if value, ok := resp.Outputs[port].(map[string]any); ok {
			if data, ok := value["data"].(string); ok {
				value["data"] = redact(data)
			}
		}
	}
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
// for any child CLI process they spawn (cursor, kiro, claude, antigravity, etc.).
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

func redact(value string) string {
	values := make([]string, 0, len(responseSecrets))
	for _, secret := range responseSecrets {
		if secret != "" {
			values = append(values, secret)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, secret := range values {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

// ReadRequestWithManifest verifies the same contract used for worker matching.
// The compiled-in manifest also protects direct adapter invocations.
func ReadRequestWithManifest(raw []byte) (*Request, error) {
	req, err := ReadRequest()
	if err != nil {
		return nil, err
	}
	manifest, err := contract.ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	if req.Executor == "" {
		req.Executor = manifest.Executor
	}
	// Operator-managed pins are supplied by the worker. Verify the embedded
	// adapter protocol independently; runner versions are re-observed locally.
	if req.Environment != nil {
		if req.Environment.ProtocolDigest != manifest.ProtocolDigest() {
			return nil, fmt.Errorf("environment adapter protocol digest mismatch")
		}
		manifest.Environment = req.Environment
	}
	if err := req.Check(manifest); err != nil {
		return nil, err
	}
	return req, nil
}
