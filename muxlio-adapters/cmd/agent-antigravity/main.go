// agent-antigravity adapter for muxlio.
//
// Reads an adapter.Request from stdin, invokes the Google Antigravity CLI (agy)
// non-interactively, collects declared output files and the final assistant
// text, and writes an adapter.Response to stdout.
//
// Params (via node.params JSON):
//
//	model   string   Optional Antigravity model override; otherwise use the CLI's configured default
//
// Special input port:
//
//	"feature"  map with title/spec/files — expanded inline into the prompt
//
// Built-in output port always populated:
//
//	"_text"    the final assistant text from the session
//
// agy CLI resolution order:
//  1. $ANTIGRAVITY_CLI or $AGY_CLI env var
//  2. ~/.local/bin/agy or ~/.local/bin/antigravity
//  3. agy or antigravity on PATH
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func main() {
	if adapter.ProbeRequested(os.Args) {
		checks := adapter.RunnerChecks(resolveAntigravityCLI(), "--version")
		checks[2] = antigravityAuthenticationCheck()
		adapter.RunProbe(checks...)
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-antigravity: %v", err)
	}
	if err := adapter.CheckRunnerEnvironment(req, resolveAntigravityCLI()); err != nil {
		adapter.Fail("agent-antigravity: %v", err)
	}

	model := ""
	if req.Params != nil {
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Params, &p); err == nil && p.Model != "" {
			model = p.Model
		}
	}

	antigravityCLI := resolveAntigravityCLI()
	workDir, err := adapter.WorkingDir(req)
	if err != nil {
		adapter.Fail("agent-antigravity: finding working directory: %v", err)
	}

	prompt := buildPrompt(req)

	// --prompt                        non-interactive scripted mode
	// --output-format                 stream-json for machine-readable streaming events
	// --dangerously-skip-permissions  allow tool execution without per-tool confirmation
	// --add-dir                       grant access to per-attempt scratch directory for outputs
	// --model                         selects the model
	policyArgs, err := adapter.AuthorizeRunner(req, antigravityCLI, permissionArgs)
	if err != nil {
		adapter.Fail("agent-antigravity: %v", err)
	}
	args := []string{
		"--prompt", prompt,
		"--output-format", "stream-json",
		"--add-dir", req.WorkDir,
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	runnerVersion := adapter.CommandCheck(contract.ProbeVersion, antigravityCLI, "--version").Version
	args = append(args, policyArgs...)
	cmd := exec.Command(antigravityCLI, args...)
	cmd.Dir = workDir
	stderr := &tailBuffer{limit: 8 * 1024}
	cmd.Stderr = stderr
	cmd.Env = adapter.CleanEnvForChild()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		adapter.Fail("agent-antigravity: stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		adapter.Fail("agent-antigravity: starting antigravity CLI: %v\n(hint: install the Google Antigravity CLI (agy) or set $ANTIGRAVITY_CLI)", err)
	}

	// Parse the newline-delimited stream-json event stream.
	var textChunks adapter.BoundedBuffer
	var runErr string
	var completed bool
	usageSequence := 0
	usageSource := ""
	conversationID := ""
	conversationConflict := false
	accountingRecorder := adapter.NewTokenRecorder("antigravity-stream")
	if model != "" {
		_ = accountingRecorder.ReportModel(model, "requested", "adapter-params")
	}

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
		typeRaw, ok := ev["event"]
		if !ok {
			typeRaw, ok = ev["type"] // tolerate older adapter test fixtures
		}
		if !ok {
			continue
		}
		var evType string
		_ = json.Unmarshal(typeRaw, &evType)

		if evType == "init" {
			var id string
			_ = json.Unmarshal(ev["conversation_id"], &id)
			if id != "" {
				if conversationID != "" && conversationID != id {
					conversationConflict = true
				}
				conversationID = id
			}
		}
		switch evType {
		case "message":
			var role string
			_ = json.Unmarshal(ev["role"], &role)
			if role == "assistant" {
				for _, chunk := range extractAssistantText(ev["message"]) {
					adapter.CaptureAgentText(&textChunks, chunk)
				}
			}
		// Antigravity CLI stream-json event types mirror the Claude Code / Cursor pattern:
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
			result := ev["result"]
			if len(result) > 0 {
				var payload map[string]json.RawMessage
				if json.Unmarshal(result, &payload) == nil {
					for k, v := range payload {
						ev[k] = v
					}
				}
			}
			var reported *adapter.TokenCounters
			if u := antigravityTokenCounters(ev["usage"]); u != nil {
				reported = u
			} else if u := antigravityTokenCounters(ev["stats"]); u != nil {
				reported = u
			}
			if err := accountingRecorder.Replace(reported); err != nil {
				adapter.AppendLogChunk("stderr", "accounting-observation-rejected")
			}
			// Collect final text if not already streamed.
			for _, key := range []string{"result", "response", "text", "content"} {
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
			var status string
			_ = json.Unmarshal(ev["status"], &status)
			if strings.EqualFold(status, "error") {
				runErr = appendFailure(runErr, "antigravity reported a terminal error")
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
			var isErr bool
			_ = json.Unmarshal(ev["is_error"], &isErr)
			if isErr {
				runErr = appendFailure(runErr, "antigravity reported a terminal error")
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
		case "step_update":
			var update stepUsageUpdate
			if json.Unmarshal(ev["step_update"], &update) == nil {
				if update.TextDelta != "" {
					adapter.CaptureAgentText(&textChunks, update.TextDelta)
				}
				if strings.EqualFold(strings.TrimSpace(update.State), "DONE") && update.ConversationID != "" && (usageSource == "" || usageSource == "steps") {
					usageSource = "steps"
					if err := observeStepUsage(accountingRecorder, update); err != nil {
						adapter.AppendLogChunk("stderr", "accounting-observation-rejected")
					}
				}
			}
		case "usage":
			if u := antigravityTokenCounters(json.RawMessage(line)); u != nil {
				if usageSource == "" || usageSource == "usage" {
					usageSource = "usage"
					usageSequence++
					if err := accountingRecorder.Delta(fmt.Sprintf("usage-%d", usageSequence), u); err != nil {
						adapter.AppendLogChunk("stderr", "accounting-observation-rejected")
					}
				}
			}
		case "completion", "done":
			completed = true
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		runErr = appendFailure(runErr, fmt.Sprintf("reading antigravity stdout: %v", err))
	}

	if err := cmd.Wait(); err != nil {
		runErr = appendFailure(runErr, fmt.Sprintf("antigravity process error: %v", err))
	}
	adapter.AppendLogChunk("stderr", stderr.String())
	if !completed {
		runErr = appendFailure(runErr, "stream ended without a completion event")
	}
	if conversationID != "" && !conversationConflict {
		_ = accountingRecorder.ReportSession(contract.AccountingSession{ID: conversationID, Provider: "google", Source: "antigravity-init", Version: runnerVersion, Isolated: true})
	}
	reason := "headless-monetary-source-unverified"
	if runnerVersion == "1.5.6" {
		reason = "headless-monetary-source-unavailable"
	}
	if conversationConflict {
		reason = "conversation-identity-conflict"
	}
	_ = accountingRecorder.SetCoverage(contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: "unspecified"}, "unknown", reason)
	finalAccounting := accountingRecorder.Finish()
	if runErr != "" {
		adapter.FailWithAccounting(finalAccounting, "agent-antigravity: %s", withStderr(runErr, stderr.String()))
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
			fmt.Sprintf("antigravity did not write expected output files: %s", strings.Join(missing, ", ")),
			stderr.String(),
		)
	}

	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, OutputErrors: outputErrors, Error: errMsg, Accounting: finalAccounting})
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

// resolveAntigravityCLI returns the path to the Antigravity CLI executable.
func resolveAntigravityCLI() string {
	if v := os.Getenv("ANTIGRAVITY_CLI"); v != "" {
		return v
	}
	if v := os.Getenv("AGY_CLI"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "agy"),
		filepath.Join(home, ".local", "bin", "antigravity"),
		"agy",
		"antigravity",
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
	}
	return filepath.Join(home, ".local", "bin", "agy")
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
