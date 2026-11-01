// command-go-test adapter for muxlio.
//
// Reads an adapter.Request from stdin. Runs Go build/test commands based on
// params:
//
//	commands  []string   commands to run in sequence (default: ["go test ./... -timeout 120s"])
//	cwd       string     working directory relative to authorized workspace (default: ".")
//
// Writes an adapter.Response to stdout with ports:
//
//	passed: {"passed": true/false, "exitCode": N}
//	output: combined stdout+stderr of all commands
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/muxlio/muxlio-adapters/adapter"
)

func main() {
	req, err := adapter.ReadRequest()
	if err != nil {
		adapter.Fail("command-go-test: %v", err)
	}

	repoRoot, err := adapter.WorkspaceRoot(req)
	if err != nil {
		adapter.Fail("command-go-test: finding repo root: %v", err)
	}

	// Parse params: optional commands and cwd override.
	type params struct {
		Commands []string `json:"commands"`
		Cwd      string   `json:"cwd"`
	}
	p := params{
		Commands: []string{"go test ./... -timeout 120s"},
		Cwd:      ".",
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &p)
		if len(p.Commands) == 0 {
			p.Commands = []string{"go test ./... -timeout 120s"}
		}
		if p.Cwd == "" {
			p.Cwd = "."
		}
	}

	runDir := filepath.Join(repoRoot, p.Cwd)

	var combined adapter.BoundedBuffer
	passed := true
	exitCode := 0

	for _, cmdStr := range p.Commands {
		cmd := exec.Command("sh", "-c", cmdStr)
		cmd.Dir = runDir

		var out adapter.BoundedBuffer
		cmd.Stdout = &out
		cmd.Stderr = &out

		runErr := cmd.Run()
		combined.WriteString(fmt.Sprintf("$ %s\n%s\n", cmdStr, out.String()))

		if runErr != nil {
			passed = false
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
			// Continue running remaining commands for full diagnostics.
		}
	}

	output := combined.String()
	outputPath := filepath.Join(req.WorkDir, "build-output.txt")
	_ = os.WriteFile(outputPath, []byte(output), 0o644)

	passedJSON, _ := json.Marshal(map[string]any{
		"passed":   passed,
		"exitCode": exitCode,
	})

	errMsg := ""
	if !passed {
		errMsg = fmt.Sprintf("commands failed (exit %d)", exitCode)
	}

	_ = adapter.WriteResponse(adapter.Response{
		Outputs: map[string]any{
			"passed": map[string]any{
				"mime": "application/json",
				"data": string(passedJSON),
			},
			"output": map[string]any{
				"mime": "text/plain",
				"path": outputPath,
				"data": output,
			},
		},
		Error: errMsg,
	})
}
