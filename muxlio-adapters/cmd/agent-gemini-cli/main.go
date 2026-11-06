// agent-gemini-cli adapter for muxlio.
//
// Reads an adapter.Request from stdin, invokes the Google Gemini CLI
// non-interactively, collects declared output files and the final assistant
// text, and writes an adapter.Response to stdout.
//
// Params (via node.params JSON):
//
//	model   string   Gemini model name, e.g. "gemini-2.0-flash", "gemini-2.5-pro" (default)
//
// Special input port:
//
//	"feature"  map with title/spec/files — expanded inline into the prompt
//
// Built-in output port always populated:
//
//	"_text"    the final assistant text from the session
//
// gemini CLI resolution order:
//  1. $GEMINI_CLI env var
//  2. ~/.local/bin/gemini
//  3. gemini on PATH
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func main() {
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-gemini-cli: %v", err)
	}

	model := "gemini-2.5-pro"
	if req.Params != nil {
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Params, &p); err == nil && p.Model != "" {
			model = p.Model
		}
	}

	geminiCLI := resolveGeminiCLI()
	repoRoot, err := adapter.WorkspaceRoot(req)
	if err != nil {
		adapter.Fail("agent-gemini-cli: finding repo root: %v", err)
	}

	prompt := buildPrompt(req)

	// --prompt         non-interactive scripted mode
	// --output-format  stream-json for machine-readable streaming events
	// --model          selects the model
	// --approval-mode  allow tool execution without per-tool confirmation
	args := []string{
		"--prompt", prompt,
		"--output-format", "stream-json",
		"--model", model,
		"--approval-mode", "yolo",
	}

	cmd := exec.Command(geminiCLI, args...)
	cmd.Dir = repoRoot
	stderr := &tailBuffer{limit: 8 * 1024}
	cmd.Stderr = stderr
	cmd.Env = adapter.CleanEnvForChild()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		adapter.Fail("agent-gemini-cli: stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		adapter.Fail("agent-gemini-cli: starting gemini CLI: %v\n(hint: install the Google Gemini CLI or set $GEMINI_CLI)", err)
	}

	// Parse the newline-delimited stream-json event stream.
	var textChunks adapter.BoundedBuffer
	var runErr string
	var completed bool
	var usage *adapter.TokenUsage

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		typeRaw, ok := ev["type"]
		if !ok {
			continue
		}
		var evType string
		_ = json.Unmarshal(typeRaw, &evType)

		switch evType {
		case "message":
			var role string
			_ = json.Unmarshal(ev["role"], &role)
			if role == "assistant" {
				for _, chunk := range extractAssistantText(json.RawMessage(line)) {
					adapter.CaptureAgentText(&textChunks, chunk)
				}
			}
		// Gemini CLI stream-json event types mirror the Claude Code / Cursor pattern:
		//   assistant  — carries message.content (array of content blocks or string)
		//   text       — carries data.content (incremental text delta)
		//   result     — terminal event, carries result/text/is_error/error
		//   completion, done — alternative terminal signals
		case "assistant":
			for _, chunk := range extractAssistantText(ev["message"]) {
				adapter.CaptureAgentText(&textChunks, chunk)
			}
		case "text":
			var payload struct {
				Content string `json:"content"`
			}
			if dataRaw, ok := ev["data"]; ok {
				_ = json.Unmarshal(dataRaw, &payload)
			}
			if payload.Content != "" {
				adapter.CaptureAgentText(&textChunks, payload.Content)
			}
		case "result":
			completed = true
			// Gemini CLI's stream-json result event carries aggregated token
			// usage. The field name depends on the CLI version: newer builds
			// emit a top-level "usage" object, while the documented headless
			// stream-json contract places aggregated counts under "stats"
			// (e.g. {"input_tokens":120,"output_tokens":18}). Try both so the
			// adapter captures usage regardless of the wire format; a nil
			// result (no recognised keys) preserves any usage reported by an
			// earlier "usage" event rather than overwriting it with absence.
			if u := adapter.ParseTokenUsage(ev["usage"]); u != nil {
				usage = u
			} else if u := adapter.ParseTokenUsage(ev["stats"]); u != nil {
				usage = u
			}
			// Collect final text if not already streamed.
			for _, key := range []string{"result", "text", "content"} {
				var s string
				if raw, ok := ev[key]; ok {
					if json.Unmarshal(raw, &s) == nil && s != "" {
						if textChunks.Len() == 0 {
							adapter.CaptureAgentText(&textChunks, s)
						}
						break
					}
				}
			}
			// Gemini's stream-json result event reports failures with status/error,
			// unlike Claude/Cursor which use is_error/subtype. Preserve both forms.
			var status string
			_ = json.Unmarshal(ev["status"], &status)
			if strings.EqualFold(status, "error") {
				runErr = appendFailure(runErr, "gemini-cli reported a terminal error")
			}
			if raw, ok := ev["error"]; ok && len(raw) > 0 && string(raw) != "null" {
				var detail struct {
					Message string `json:"message"`
				}
				if json.Unmarshal(raw, &detail) == nil && detail.Message != "" {
					runErr = appendFailure(runErr, detail.Message)
				} else {
					var s string
					if json.Unmarshal(raw, &s) == nil && s != "" {
						runErr = appendFailure(runErr, s)
					}
				}
			}
			// Detect terminal errors used by other stream-json CLI schemas.
			var isErr bool
			_ = json.Unmarshal(ev["is_error"], &isErr)
			if isErr {
				runErr = appendFailure(runErr, "gemini-cli reported a terminal error")
			}
			for _, key := range []string{"subtype"} {
				if raw, ok := ev[key]; ok && len(raw) > 0 && string(raw) != "null" {
					var s string
					if json.Unmarshal(raw, &s) != nil {
						s = strings.TrimSpace(string(raw))
					}
					if s == "" {
						continue
					}
					if key == "subtype" && !strings.Contains(strings.ToLower(s), "error") {
						continue
					}
					runErr = appendFailure(runErr, s)
					break
				}
			}
		case "usage":
			// Newer Gemini CLI builds emit a dedicated "usage" event whose
			// provider-reported counts live at the top level of the event
			// object (camelCase: inputTokens, outputTokens, cachedTokens, or
			// the raw usageMetadata names promptTokenCount, etc.). Parse the
			// whole line so ParseTokenUsage sees those top-level keys. The
			// last "usage" event holds the final total, so overwrite each
			// time a non-nil value is parsed.
			if u := adapter.ParseTokenUsage(json.RawMessage(line)); u != nil {
				usage = u
			}
		case "completion", "done":
			completed = true
			// No extra action needed; process exit determines success.
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		runErr = appendFailure(runErr, fmt.Sprintf("reading gemini-cli stdout: %v", err))
	}

	if err := cmd.Wait(); err != nil {
		runErr = appendFailure(runErr, fmt.Sprintf("gemini-cli process error: %v", err))
	}
	adapter.AppendLogChunk("stderr", stderr.String())
	if !completed {
		runErr = appendFailure(runErr, "stream ended without a completion event")
	}
	if runErr != "" {
		// Preserve provider-reported usage from the terminal result/usage
		// event so failed attempts that still returned usage are recorded,
		// not estimated as zero/unavailable. usage is nil when no usage
		// event was parsed, which omitempty reports as absent (matching
		// Fail).
		adapter.FailWithUsage(usage, "agent-gemini-cli: %s", withStderr(runErr, stderr.String()))
	}

	outputs, missing := adapter.CollectOutputs(req)

	// Always populate "_text" with the complete assistant response.
	outputs["_text"] = map[string]any{
		"mime": "text/plain",
		"data": textChunks.String(),
	}

	errMsg := ""
	if len(missing) > 0 {
		errMsg = withStderr(
			fmt.Sprintf("gemini-cli did not write expected output files: %s", strings.Join(missing, ", ")),
			stderr.String(),
		)
	}

	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, Error: errMsg, Usage: usage})
}

// extractAssistantText pulls text from a "message" JSON blob.
// Supports both a plain string content and an array of content blocks.
func extractAssistantText(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return nil
	}
	// Try plain string.
	var s string
	if json.Unmarshal(msg.Content, &s) == nil && s != "" {
		return []string{s}
	}
	// Try array of content blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(msg.Content, &blocks) != nil {
		return nil
	}
	var chunks []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			chunks = append(chunks, b.Text)
		}
	}
	return chunks
}

// buildPrompt constructs the full prompt from the request, expanding the
// optional "feature" input and declaring output file paths.
func buildPrompt(req *adapter.Request) string {
	var sb strings.Builder
	sb.WriteString(req.Task)
	sb.WriteString("\n\nWorkspace rule: keep all generated notes, plans, scratch files, and other artifacts inside the run workspace at ")
	sb.WriteString(req.WorkDir)
	sb.WriteString(". Do not create incidental files in the source repository. If an output is declared below, write it to that exact path.")

	// Expand "feature" input.
	if fv, ok := req.Inputs["feature"]; ok {
		switch feature := fv.(type) {
		case map[string]any:
			sb.WriteString("\n")
			if title, _ := feature["title"].(string); title != "" {
				sb.WriteString(fmt.Sprintf("\nFeature title: %s", title))
			}
			if files, ok := feature["files"].([]any); ok && len(files) > 0 {
				sb.WriteString("\nFiles to read first:")
				for _, f := range files {
					if s, ok := f.(string); ok {
						sb.WriteString(fmt.Sprintf("\n  - %s", s))
					}
				}
			}
			if spec, _ := feature["spec"].(string); spec != "" {
				sb.WriteString(fmt.Sprintf("\n\nSpec:\n%s", spec))
			}
		case string:
			sb.WriteString(fmt.Sprintf("\n\nFeature input:\n%s", feature))
		}
	}

	// Expand any non-feature input port carrying a "data" string.
	for name, val := range req.Inputs {
		if name == "feature" {
			continue
		}
		switch v := val.(type) {
		case map[string]any:
			if data, _ := v["data"].(string); data != "" {
				sb.WriteString(fmt.Sprintf("\n\n--- %s ---\n%s", name, data))
			}
		case string:
			sb.WriteString(fmt.Sprintf("\n\n--- %s ---\n%s", name, v))
		}
	}

	// Declare output file paths.
	if len(req.Outputs) > 0 {
		sb.WriteString("\n\nIMPORTANT: write these output files using their exact absolute paths:")
		for port, decl := range req.Outputs {
			name := decl.Path
			if name == "" {
				name = port
			}
			sb.WriteString(fmt.Sprintf("\n  %s/%s  (mime: %s)", req.WorkDir, name, decl.Mime))
		}
	}

	sb.WriteString(fmt.Sprintf("\n\nNode ID: %s", req.NodeID))
	return sb.String()
}

// resolveGeminiCLI returns the path to the Gemini CLI executable.
func resolveGeminiCLI() string {
	if v := os.Getenv("GEMINI_CLI"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "gemini"),
		"gemini",
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
	}
	return filepath.Join(home, ".local", "bin", "gemini")
}

// tailBuffer retains only the most recent limit bytes written to it.
type tailBuffer struct {
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.limit <= 0 {
		return n, nil
	}
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return n, nil
	}
	if excess := len(b.data) + len(p) - b.limit; excess > 0 {
		copy(b.data, b.data[excess:])
		b.data = b.data[:len(b.data)-excess]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.data) }

func withStderr(reason, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return reason
	}
	return fmt.Sprintf("%s (stderr: %s)", reason, stderr)
}

func appendFailure(current, next string) string {
	if current == "" {
		return next
	}
	return current + "; " + next
}
