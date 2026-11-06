// agent-kiro adapter for muxlio.
//
// Reads an adapter.Request from stdin, invokes kiro-cli non-interactively,
// collects output files and the final assistant text, writes adapter.Response.
//
// Params (via node.params JSON):
//
//	model   string   kiro model name, e.g. "claude-sonnet-4.5", "auto" (default)
//
// Special input port:
//
//	"feature"  map with title/spec/files — expanded inline into the prompt
//
// Built-in output port always populated:
//
//	"_text"    the last assistant text message from the session
//
// kiro-cli resolution order:
//  1. $KIRO_CLI env var
//  2. ~/.local/bin/kiro-cli
//  3. kiro-cli / kiro on PATH
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
	if adapter.ProbeRequested(os.Args) {
		adapter.RunProbe(adapter.RunnerChecks(resolveKiroCLI(), "whoami")...)
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-kiro: %v", err)
	}

	model := "auto"
	if req.Params != nil {
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Params, &p); err == nil && p.Model != "" {
			model = p.Model
		}
	}

	kiroCLI := resolveKiroCLI()
	workDir, err := adapter.WorkingDir(req)
	if err != nil {
		adapter.Fail("agent-kiro: finding working directory: %v", err)
	}

	prompt := adapter.ComposePrompt(req)

	args := []string{
		"chat",
		"--no-interactive",
		"--trust-all-tools",
		"--trust-workspace",
		"--output-format", "stream-json",
		"--model", model,
		prompt,
	}

	cmd := exec.Command(kiroCLI, args...)
	cmd.Dir = workDir
	stderr := &tailBuffer{limit: 8 * 1024}
	cmd.Stderr = stderr
	cmd.Env = adapter.CleanEnvForChild()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		adapter.Fail("agent-kiro: stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		adapter.Fail("agent-kiro: starting kiro-cli: %v", err)
	}

	// Parse the stream-json event stream.
	// Collect all assistant text chunks; watch for runFinished.
	var textChunks adapter.BoundedBuffer
	var runErr string
	var completed bool
	var usage *adapter.TokenUsage
	stdoutTail := &tailBuffer{limit: 32 * 1024}

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MB line buffer
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
		// ACP reports turn metadata on sessionUpdate before runFinished. The
		// final event may carry no usage; do not discard already reported counts.
		if evType == "sessionUpdate" || evType == "runFinished" || evType == "runError" {
			if reported := parseKiroUsage(ev["data"]); reported != nil {
				usage = reported
			}
		}

		switch evType {
		case "sessionUpdate":
			var payload struct {
				Update struct {
					Kind    string `json:"sessionUpdate"`
					Content struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"update"`
			}
			if raw, ok := ev["data"]; ok {
				_ = json.Unmarshal(raw, &payload)
			}
			if payload.Update.Kind == "agent_message_chunk" && payload.Update.Content.Type == "text" {
				adapter.CaptureAgentText(&textChunks, payload.Update.Content.Text)
			}
		case "text":
			// Incremental assistant text.
			var payload struct {
				Content string `json:"content"`
			}
			if dataRaw, ok := ev["data"]; ok {
				_ = json.Unmarshal(dataRaw, &payload)
			}
			if payload.Content != "" {
				adapter.CaptureAgentText(&textChunks, payload.Content)
			}
		case "runFinished":
			var payload struct {
				Status    string `json:"status"`
				FinalText string `json:"finalText"`
				ExitCode  *int   `json:"exitCode"`
				Error     string `json:"error"`
			}
			dataRaw := ev["data"]
			if len(dataRaw) == 0 || strings.TrimSpace(string(dataRaw)) == "null" || json.Unmarshal(dataRaw, &payload) != nil {
				runErr = appendFailure(runErr, "malformed kiro runFinished data")
				continue
			}
			completed = true
			if payload.Status != "" && payload.Status != "success" {
				runErr = fmt.Sprintf("kiro run finished with status %s", payload.Status)
			}
			if textChunks.Len() == 0 && payload.FinalText != "" {
				adapter.CaptureAgentText(&textChunks, payload.FinalText)
			}
			if payload.ExitCode != nil && *payload.ExitCode != 0 {
				runErr = fmt.Sprintf("kiro-cli exited with code %d", *payload.ExitCode)
			}
			if payload.Error != "" {
				runErr = payload.Error
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		runErr = appendFailure(runErr, fmt.Sprintf("reading kiro-cli stdout: %v", err))
	}

	if err := cmd.Wait(); err != nil {
		runErr = appendFailure(runErr, fmt.Sprintf("kiro-cli process error: %v", err))
	}
	adapter.AppendLogChunk("stderr", stderr.String())
	if !completed {
		runErr = appendFailure(runErr, "stream ended without a completion event")
	}
	if runErr != "" {
		diagnosticPath := filepath.Join(req.WorkDir, "kiro-stream.jsonl")
		_ = os.WriteFile(diagnosticPath, []byte(stdoutTail.String()), 0o600)
		runErr = appendFailure(runErr, "stdout tail: "+stdoutTail.String())
		adapter.FailWithUsage(usage, "agent-kiro: %s", withStderr(runErr, stderr.String()))
	}

	// Collect declared outputs. Scalar ports (no file path) are returned as
	// parsed JSON values; file-artifact ports as {mime, path, data} maps.
	outputs, missing, outputErrors := adapter.CollectOutputs(req)

	// Always populate the special "_text" port with the full assistant response.
	fullText := textChunks.String()
	outputs["_text"] = map[string]any{
		"mime": "text/plain",
		"data": fullText,
	}

	errMsg := ""
	if len(missing) > 0 {
		errMsg = withStderr(
			fmt.Sprintf("kiro did not write expected output files: %s", strings.Join(missing, ", ")),
			stderr.String(),
		)
	}

	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, OutputErrors: outputErrors, Error: errMsg, Usage: usage})
}

func parseKiroUsage(result json.RawMessage) *adapter.TokenUsage {
	var payload map[string]json.RawMessage
	if json.Unmarshal(result, &payload) != nil {
		return nil
	}
	for _, key := range []string{"tokenUsage", "usage", "metrics"} {
		if usage := adapter.ParseTokenUsage(payload[key]); usage != nil {
			return usage
		}
	}
	if usage := adapter.ParseTokenUsage(result); usage != nil {
		return usage
	}
	// Follow only protocol metadata containers, never tool output, credit
	// summaries, or context-window percentages (none are token usage).
	for _, key := range []string{"metadata", "update", "_meta", "kiro"} {
		if usage := parseKiroUsage(payload[key]); usage != nil {
			return usage
		}
	}
	return nil
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

func resolveKiroCLI() string {
	if v := os.Getenv("KIRO_CLI"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "kiro-cli"),
		"kiro-cli",
		"kiro",
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
	}
	return filepath.Join(home, ".local", "bin", "kiro-cli")
}
