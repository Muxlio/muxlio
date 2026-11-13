// agent-codex adapter for muxlio.
//
// Reads an adapter.Request from stdin, builds a prompt from the task and
// any "feature" input, runs `codex exec --json --ephemeral` with operator authorization,
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

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

func main() {
	if adapter.ProbeRequested(os.Args) {
		adapter.RunProbe(runnerChecks("codex", "login", "status")...)
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
	var params struct {
		Model string `json:"model"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			adapter.Fail("agent-codex: invalid params: %v", err)
		}
	}

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
	args, err := adapter.AuthorizeRunner(req, "codex", func(p *contract.AuthorizationPolicy, version string) ([]string, error) {
		return invocationArgs(p, version, params.Model, req.WorkDir, prompt)
	})
	if err != nil {
		adapter.Fail("agent-codex: %v", err)
	}
	runnerVersion := adapter.CommandCheck(contract.ProbeVersion, "codex", "--version").Version
	cmd := exec.Command("codex", args...)
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
	var assistant codexAgentText
	var runErr string
	var completed bool
	recorder := accounting.NewRecorder()
	if params.Model != "" {
		_ = recorder.ReportModel(contract.AccountingModel{ID: params.Model, Role: "requested", Source: "adapter-params"})
	}
	publisher := adapter.NewAccountingPublisher()
	_ = recorder.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "unknown", "no-confirmed-report")
	_ = recorder.SetCoverage(contract.AccountingKey{Kind: "cost", Currency: "USD", Basis: "api-equivalent"}, "unknown", "served-model-and-tariff-unverified")
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
		case "thread.started":
			var threadID string
			_ = json.Unmarshal(ev["thread_id"], &threadID)
			if threadID != "" {
				_ = recorder.ReportSession(contract.AccountingSession{ID: threadID, Provider: "openai", Source: "codex-exec-thread", Version: runnerVersion, Isolated: true})
			}
		case "item.completed":
			// A turn may emit several agent_message items (commentary,
			// final_answer, or phase-less). Concatenating them misstates the
			// answer; match Codex SDK final-answer selection instead.
			var item struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Phase string `json:"phase"`
			}
			if raw, ok := ev["item"]; ok {
				_ = json.Unmarshal(raw, &item)
			}
			if item.Type == "agent_message" {
				assistant.observe(item.Phase, item.Text)
			}
		case "turn.completed":
			completed = true
			// turn.completed carries the thread's running token total. With
			// --ephemeral the thread is fresh, so this is the run's usage.
			// When multiple turns complete, the last holds the final total,
			// so overwrite each time.
			if reported, decodeErr := codexTokenMeasures(ev["usage"]); decodeErr != nil {
				adapter.AppendLogChunk("stderr", "accounting-observation-rejected")
			} else if reported != nil {
				part := accounting.Part{Scope: "ephemeral-thread", Entry: contract.AccountingEntry{ID: "thread", Tokens: reported}}
				if err := recorder.ObserveCumulative(part); err != nil {
					adapter.AppendLogChunk("stderr", "accounting-observation-rejected: confirmed progress remains partial")
				} else {
					_ = recorder.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "partial", "source-coverage-unverified")
					if snapshot, err := recorder.Snapshot(); err == nil {
						_ = publisher.Publish(*snapshot, true)
					}
				}
			}
		case "turn.failed":
			// An earlier confirmed report remains a subtotal on failure.
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
	finalAccounting, accountingErr := adapter.FinalizeAccounting(recorder, publisher)
	if accountingErr != nil {
		adapter.AppendLogChunk("stderr", "accounting-finalization-failed: confirmed progress remains partial")
	}
	if runErr != "" {
		runErr = adapter.RunnerFailure(runErr, stderr.String()+"\n"+stdoutTail.String(), "codex login")
		runErr = appendFailure(runErr, "stdout tail: "+stdoutTail.String())
		// Preserve provider-reported usage from turn.completed so failed
		// attempts that still returned usage are recorded, not estimated
		// as zero/unavailable. Accounting is empty when no turn.completed was
		// parsed, which omitempty reports as absent (matching Fail).
		adapter.FailWithAccounting(finalAccounting, "agent-codex: %s", withStderr(runErr, stderr.String()))
	}

	// Collect declared outputs. Scalar ports (no file path) are returned as
	// parsed JSON values; file-artifact ports as {mime, path, data} maps.
	outputs, missing, outputErrors := adapter.CollectOutputs(req)
	outputs["_text"] = map[string]any{"mime": "text/plain", "data": assistant.result()}

	errMsg := ""
	if len(missing) > 0 {
		errMsg = fmt.Sprintf("codex did not write expected output files: %s", strings.Join(missing, ", "))
	}

	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, OutputErrors: outputErrors, Error: errMsg, Accounting: finalAccounting})
}

// codexAgentText tracks assistant messages from the Codex JSONL stream.
// Prefer final_answer; otherwise the last non-empty message with no phase.
// Empty agent_message items are ignored so a trailing blank does not erase the answer.
type codexAgentText struct {
	finalAnswer string
	hasFinal    bool
	lastNoPhase string
}

func (s *codexAgentText) observe(phase, text string) {
	if text == "" {
		return
	}
	adapter.AppendStreamText(text)
	switch phase {
	case "commentary":
		// Commentary is useful live progress, but is not the turn answer.
	case "final_answer":
		s.finalAnswer = text
		s.hasFinal = true
	case "":
		s.lastNoPhase = text
	}
}

func (s *codexAgentText) result() string {
	if s.hasFinal {
		return s.finalAnswer
	}
	return s.lastNoPhase
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
