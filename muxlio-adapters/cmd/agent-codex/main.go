// agent-codex adapter for muxlio.
//
// Reads an adapter.Request from stdin, builds a prompt from the task and
// any "feature" input, runs `codex exec --json --approve-for-me --ephemeral`,
// parses the JSONL event stream to collect assistant text and provider-reported
// token usage, collects declared output files from workDir, and writes an
// adapter.Response to stdout.
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
		adapter.RunProbe(adapter.RunnerChecks("codex", "login", "status")...)
		return
	}
	req, err := adapter.ReadRequestWithManifest(manifest)
	if err != nil {
		adapter.Fail("agent-codex: %v", err)
	}
	if err := adapter.CheckRunnerEnvironment(req, "codex"); err != nil {
		adapter.Fail("agent-codex: %v", err)
	}

	prompt := adapter.ComposePrompt(req)

	// Run codex in the per-step workspace when one is supplied, otherwise the
	// authorized workspace. Attempt snapshots deliberately omit Git metadata.
	workDir, err := adapter.WorkingDir(req)
	if err != nil {
		adapter.Fail("agent-codex: finding working directory: %v", err)
	}

	// --json switches stdout to a JSON Lines event stream so the adapter can
	// capture assistant text and provider-reported token usage. --ephemeral
	// keeps each run on a fresh thread, so the turn.completed usage total is
	// this run's usage rather than a resumed thread's running total.
	cmd := exec.Command(
		"codex", "exec",
		"--json",
		"--approve-for-me",
		"--ephemeral",
		"--skip-git-repo-check",
		"--add-dir", req.WorkDir,
		"-c", "shell_environment_policy.inherit=all",
		prompt,
	)
	cmd.Dir = workDir
	cmd.Env = adapter.CleanEnvForChild()

	stderr := &tailBuffer{limit: 8 * 1024}
	cmd.Stderr = stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		adapter.Fail("agent-codex: stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		adapter.Fail("agent-codex: starting codex: %v", err)
	}

	// Parse the newline-delimited JSONL event stream emitted by `codex exec
	// --json`. Each line is a top-level thread event keyed by "type":
	//   thread.started, turn.started, turn.completed, turn.failed, error,
	//   item.started, item.updated, item.completed.
	var textChunks adapter.BoundedBuffer
	var runErr string
	var completed bool
	var usage *adapter.TokenUsage
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
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		typeRaw, ok := ev["type"]
		if !ok {
			continue
		}
		var evType string
		_ = json.Unmarshal(typeRaw, &evType)

		switch evType {
		case "item.completed":
			// agent_message items carry the assistant's final answer text;
			// capture it for live streaming.
			var item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if raw, ok := ev["item"]; ok {
				_ = json.Unmarshal(raw, &item)
			}
			if item.Type == "agent_message" && item.Text != "" {
				adapter.CaptureAgentText(&textChunks, item.Text)
			}
		case "turn.completed":
			completed = true
			// turn.completed carries the thread's running token total. With
			// --ephemeral the thread is fresh, so this is the run's usage.
			// When multiple turns complete, the last holds the final total,
			// so overwrite each time.
			if reported := adapter.ParseTokenUsage(ev["usage"]); reported != nil {
				usage = reported
			}
		case "turn.failed":
			// A failed turn reports no usage (its tokens land in the next
			// run's total), but an earlier turn.completed may have reported
			// usage, which is preserved via FailWithUsage below.
			var errObj struct {
				Message string `json:"message"`
			}
			if raw, ok := ev["error"]; ok {
				_ = json.Unmarshal(raw, &errObj)
			}
			if errObj.Message != "" {
				runErr = appendFailure(runErr, errObj.Message)
			} else {
				runErr = appendFailure(runErr, "codex turn failed")
			}
		case "error":
			// Top-level error events may be transient reconnect notices; treat
			// "Reconnecting..." as non-fatal progress, everything else as fatal.
			var msg string
			_ = json.Unmarshal(ev["message"], &msg)
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg)), "reconnecting") {
				continue
			}
			if msg != "" {
				runErr = appendFailure(runErr, msg)
			} else {
				runErr = appendFailure(runErr, "codex stream error")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		runErr = appendFailure(runErr, fmt.Sprintf("reading codex stdout: %v", err))
	}

	if err := cmd.Wait(); err != nil {
		runErr = appendFailure(runErr, fmt.Sprintf("codex exited with error: %v", err))
	}
	adapter.AppendLogChunk("stderr", stderr.String())
	if !completed {
		runErr = appendFailure(runErr, "stream ended without a completion event")
	}
	if runErr != "" {
		runErr = appendFailure(runErr, "stdout tail: "+stdoutTail.String())
		// Preserve provider-reported usage from turn.completed so failed
		// attempts that still returned usage are recorded, not estimated
		// as zero/unavailable. usage is nil when no turn.completed was
		// parsed, which omitempty reports as absent (matching Fail).
		adapter.FailWithUsage(usage, "agent-codex: %s", withStderr(runErr, stderr.String()))
	}

	// Collect declared outputs. Scalar ports (no file path) are returned as
	// parsed JSON values; file-artifact ports as {mime, path, data} maps.
	outputs, missing, outputErrors := adapter.CollectOutputs(req)
	outputs["_text"] = map[string]any{"mime": "text/plain", "data": textChunks.String()}

	errMsg := ""
	if len(missing) > 0 {
		errMsg = fmt.Sprintf("codex did not write expected output files: %s", strings.Join(missing, ", "))
	}

	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, OutputErrors: outputErrors, Error: errMsg, Usage: usage})
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
