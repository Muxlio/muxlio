// agent-cursor adapter for muxlio.
//
// Reads an adapter.Request from stdin, invokes Cursor's agent CLI in
// stream-json mode, collects declared output files and assistant text, and
// writes an adapter.Response.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func main() {
	if adapter.ProbeRequested(os.Args) {
		cli, _ := resolveCursorAgent()
		adapter.RunProbe(adapter.RunnerChecks(cli, "status")...)
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-cursor: %v", err)
	}

	model := "composer-2.5"
	if req.Params != nil {
		var params struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(req.Params, &params) == nil && params.Model != "" {
			model = params.Model
		}
	}

	cli, err := resolveCursorAgent()
	if err != nil {
		adapter.Fail("agent-cursor: %v", err)
	}
	if err := adapter.CheckRunnerEnvironment(req, cli); err != nil {
		adapter.Fail("agent-cursor: %v", err)
	}
	workDir, err := adapter.WorkingDir(req)
	if err != nil {
		adapter.Fail("agent-cursor: finding working directory: %v", err)
	}

	cmd := exec.Command(cli, "--print", "--yolo", "--trust", "--output-format", "stream-json", "--model", model, adapter.ComposePrompt(req))
	cmd.Dir = workDir
	var diagnostics adapter.BoundedBuffer
	cmd.Stderr = &diagnostics
	cmd.Env = cursorAgentEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		adapter.Fail("agent-cursor: stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		adapter.Fail("agent-cursor: starting agent: %v", err)
	}

	var textChunks adapter.BoundedBuffer
	var streamErr string
	completed := false
	var usage *adapter.TokenUsage
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		var eventType string
		if json.Unmarshal(event["type"], &eventType) != nil {
			continue
		}
		switch eventType {
		case "assistant":
			for _, text := range assistantText(event["message"]) {
				adapter.CaptureAgentText(&textChunks, text)
			}
		case "text":
			var data struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(event["data"], &data)
			if data.Content != "" {
				adapter.CaptureAgentText(&textChunks, data.Content)
			}
		case "result":
			completed = true
			if reported := adapter.ParseTokenUsage(event["usage"]); reported != nil {
				usage = reported
			}
			if textChunks.Len() == 0 {
				for _, text := range resultText(event) {
					adapter.CaptureAgentText(&textChunks, text)
				}
			}
			if msg := resultError(event); msg != "" {
				streamErr = msg
			}
		case "completion", "done":
			completed = true
		}
	}
	readErr := scanner.Err()
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	adapter.AppendLogChunk("stderr", diagnostics.String())
	fail := func(format string, args ...any) {
		_ = adapter.WriteResponse(adapter.Response{Error: fmt.Sprintf(format, args...), Logs: diagnostics.String(), Usage: usage})
	}
	if readErr != nil {
		fail("agent-cursor: reading stream: %v", readErr)
		return
	}
	if !completed {
		fail("agent-cursor: stream ended without a completion event")
		return
	}
	if streamErr != "" {
		fail("agent-cursor: %s", streamErr)
		return
	}
	if waitErr != nil {
		fail("agent-cursor: agent exited with error: %v", waitErr)
		return
	}

	// Collect declared outputs. Scalar ports (no file path) are returned as
	// parsed JSON values; file-artifact ports as {mime, path, data} maps.
	outputs, missing, outputErrors := adapter.CollectOutputs(req)
	outputs["_text"] = map[string]any{"mime": "text/plain", "data": textChunks.String()}
	errMsg := ""
	if len(missing) > 0 {
		errMsg = fmt.Sprintf("agent-cursor did not write expected output files: %s", strings.Join(missing, ", "))
	}
	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, OutputErrors: outputErrors, Error: errMsg, Logs: diagnostics.String(), Usage: usage})
}

func assistantText(raw json.RawMessage) []string {
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return nil
	}
	var direct string
	if json.Unmarshal(message.Content, &direct) == nil {
		if direct != "" {
			return []string{direct}
		}
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		return nil
	}
	var chunks []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			chunks = append(chunks, block.Text)
		}
	}
	return chunks
}

func resultText(event map[string]json.RawMessage) []string {
	for _, key := range []string{"result", "text", "content"} {
		var s string
		if json.Unmarshal(event[key], &s) == nil && s != "" {
			return []string{s}
		}
	}
	if msg, ok := event["message"]; ok {
		return assistantText(msg)
	}
	return nil
}

func resultError(event map[string]json.RawMessage) string {
	var failed bool
	_ = json.Unmarshal(event["is_error"], &failed)
	if failed {
		return "Cursor reported a terminal error"
	}
	for _, key := range []string{"error", "subtype"} {
		raw := event[key]
		var value string
		if len(raw) != 0 && string(raw) != "null" {
			if json.Unmarshal(raw, &value) != nil {
				value = strings.TrimSpace(string(raw))
			}
			if value == "" {
				continue
			}
			if key == "subtype" && !strings.Contains(strings.ToLower(value), "error") {
				continue
			}
			return value
		}
	}
	return ""
}

func cursorAgentEnv() []string {
	env := adapter.CleanEnvForChild()
	// Force on-disk credential storage so the Cursor agent CLI does not attempt
	// to unlock the macOS keychain in headless environments (SSH, launchd).
	// The muxlio worker also injects this variable via script.go environment();
	// setting it here ensures the same behaviour when the adapter is invoked
	// directly (e.g. during development or in standalone tests).
	return upsertEnvVar(env, "AGENT_CLI_CREDENTIAL_STORE", "file")
}

func upsertEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+value)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}

func resolveCursorAgent() (string, error) {
	if path := os.Getenv("CURSOR_AGENT"); path != "" {
		return path, nil
	}
	path, err := exec.LookPath("agent")
	if err != nil {
		return "", fmt.Errorf("could not find Cursor agent executable (set CURSOR_AGENT or add agent to PATH): %w", err)
	}
	return path, nil
}
