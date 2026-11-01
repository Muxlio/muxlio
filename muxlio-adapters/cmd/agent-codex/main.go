// agent-codex adapter for muxlio.
//
// Reads an adapter.Request from stdin, builds a prompt from the task and
// any "feature" input, runs `codex exec --approve-for-me --ephemeral`,
// collects declared output files from workDir, and writes an
// adapter.Response to stdout.
package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func main() {
	req, err := adapter.ReadRequest()
	if err != nil {
		adapter.Fail("agent-codex: %v", err)
	}

	prompt := adapter.ComposePrompt(req)

	// Run codex in the per-step workspace when one is supplied (e.g. a git
	// worktree from a prior "prepare" step), otherwise the authorized workspace.
	workDir, err := adapter.WorkingDir(req)
	if err != nil {
		adapter.Fail("agent-codex: finding working directory: %v", err)
	}

	cmd := exec.Command(
		"codex", "exec",
		"--approve-for-me",
		"--ephemeral",
		"-c", "shell_environment_policy.inherit=all",
		prompt,
	)
	cmd.Dir = workDir
	cmd.Env = adapter.CleanEnvForChild()

	var stdout, stderr adapter.BoundedBuffer
	cmd.Stderr = &stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		adapter.Fail("agent-codex: stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		adapter.Fail("agent-codex: starting codex: %v", err)
	}

	buf := make([]byte, 4096)
	for {
		n, readErr := stdoutPipe.Read(buf)
		if n > 0 {
			adapter.CaptureAgentText(&stdout, string(buf[:n]))
		}
		if readErr != nil {
			break
		}
	}

	runErr := cmd.Wait()
	if runErr != nil {
		adapter.Fail("agent-codex: codex exited with error: %v\nstderr: %s", runErr, stderr.String())
	}

	// Collect declared outputs. Scalar ports (no file path) are returned as
	// parsed JSON values; file-artifact ports as {mime, path, data} maps.
	outputs, missing := adapter.CollectOutputs(req)

	errMsg := ""
	if len(missing) > 0 {
		errMsg = fmt.Sprintf("codex did not write expected output files: %s", strings.Join(missing, ", "))
	}

	_ = adapter.WriteResponse(adapter.Response{Outputs: outputs, Error: errMsg})
}
