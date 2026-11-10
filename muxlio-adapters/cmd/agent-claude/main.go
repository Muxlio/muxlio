// agent-claude adapter for muxlio.
//
// Reads an adapter.Request from stdin, invokes the Anthropic Claude CLI
// non-interactively, collects declared output files and the final assistant
// text, and writes an adapter.Response to stdout.
//
// Params (via node.params JSON):
//
//	model   string   Claude model name, e.g. "claude-opus-4-5", "claude-sonnet-4-5" (default)
//
// Special input port:
//
//	"feature"  map with title/spec/files — expanded inline into the prompt
//
// Built-in output port always populated:
//
//	"_text"    the last assistant text from the session
//
// claude CLI resolution order:
//  1. $CLAUDE_CLI env var
//  2. ~/.local/bin/claude
//  3. claude on PATH
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
	"github.com/muxlio/muxlio-adapters/contract"
)

func main() {
	if adapter.ProbeRequested(os.Args) {
		adapter.RunProbe(adapter.RunnerChecks(resolveClaudeCLI(), "auth", "status")...)
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-claude: %v", err)
	}
	if err := adapter.CheckRunnerEnvironment(req, resolveClaudeCLI()); err != nil {
		adapter.Fail("agent-claude: %v", err)
	}

	model := "claude-sonnet-4-5"
	if req.Params != nil {
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Params, &p); err == nil && p.Model != "" {
			model = p.Model
		}
	}

	claudeCLI := resolveClaudeCLI()
	workDir, err := adapter.WorkingDir(req)
	if err != nil {
		adapter.Fail("agent-claude: finding working directory: %v", err)
	}

	prompt := buildPrompt(req)

	// --print          non-interactive scripted mode
	// --output-format  stream-json for machine-readable streaming events
	// --model          selects the model
	// --verbose        required by stream-json in print mode
	// --add-dir        allow declared outputs in the separate scratch directory
	args := []string{
		"--add-dir", req.WorkDir,
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--model", model,
		prompt,
	}

	cmd := exec.Command(claudeCLI, args...)
	cmd.Dir = workDir
	stderr := &tailBuffer{limit: 8 * 1024}
	cmd.Stderr = stderr
	cmd.Env = adapter.CleanEnvForChild()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		adapter.Fail("agent-claude: stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		adapter.Fail("agent-claude: starting claude CLI: %v\n(hint: install the Anthropic Claude CLI or set $CLAUDE_CLI)", err)
	}

	// Parse the newline-delimited stream-json event stream.
	var textChunks adapter.BoundedBuffer
	var runErr string
	completed := false
	var usage *adapter.TokenUsage
	var measurements *contract.Measurements
	stdoutTail := &tailBuffer{limit: 32 * 1024}

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		_, _ = stdoutTail.Write([]byte(line + "\n"))
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
		// Claude Code CLI stream-json event types:
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
			if reported := parseClaudeTokenUsage(ev["usage"]); reported != nil {
				usage = reported
			}
			if estimated := parseClaudeEstimatedCostUSD(ev); estimated != nil {
				measurements = withEstimatedCost(measurements, estimated)
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
			// Detect terminal errors.
			var isErr bool
			_ = json.Unmarshal(ev["is_error"], &isErr)
			if isErr {
				runErr = appendFailure(runErr, "claude reported a terminal error")
			}
			for _, key := range []string{"error", "subtype"} {
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
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		runErr = appendFailure(runErr, fmt.Sprintf("reading claude stdout: %v", err))
	}

	if err := cmd.Wait(); err != nil {
		runErr = appendFailure(runErr, fmt.Sprintf("claude process error: %v", err))
	}
	adapter.AppendLogChunk("stderr", stderr.String())
	if !completed {
		runErr = appendFailure(runErr, "claude stream ended without a result event")
	}
	if runErr != "" {
		runErr = appendFailure(runErr, "stdout tail: "+stdoutTail.String())
		// Preserve provider-reported usage from the terminal result event so
		// failed attempts that still returned usage are recorded, not estimated
		// as zero/unavailable. usage is nil when no result event was parsed.
		adapter.FailWithObserved(usage, measurements, "agent-claude: %s", withStderr(runErr, stderr.String()))
	}

	outputs, missing, outputErrors := adapter.CollectOutputs(req)

	// Always populate "_text" with the complete assistant response.
	outputs["_text"] = map[string]any{
		"mime": "text/plain",
		"data": textChunks.String(),
	}

	errMsg := ""
	if len(missing) > 0 {
		errMsg = withStderr(
			fmt.Sprintf("claude did not write expected output files: %s", strings.Join(missing, ", ")),
			stderr.String(),
		)
	}

	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, OutputErrors: outputErrors, Error: errMsg, Usage: usage, Measurements: measurements})
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
	return adapter.ComposePrompt(req)
}

// resolveClaudeCLI returns the path to the Claude CLI executable.
func resolveClaudeCLI() string {
	if v := os.Getenv("CLAUDE_CLI"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "claude"),
		"claude",
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
	}
	return filepath.Join(home, ".local", "bin", "claude")
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
