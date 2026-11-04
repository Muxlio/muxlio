package plan_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// Compile the SDK condition and exercise it with real subprocess results,
// including capture of passed=false into the fix branch and the final gate.
func TestCommandPassedRepeatUntilEndToEnd(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/test/ambient-agent.sock")
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules/tsx")); err != nil {
		t.Skip("tsx unavailable; install workspace dependencies")
	}
	compile := exec.Command("node", "--import", "tsx", "--input-type=module", "-e", `
import { workflow, task } from './muxlio-sdk/src/workflow.ts';
import { z } from 'zod';
const command = (args, extra = {}) => task({
  executor: 'command',
  params: { program: process.execPath, args, cwd: '.', onNonZero: 'return', ...extra },
});
const verify = command(['-e', "if(process.env.SSH_AUTH_SOCK!=='/test/worker-agent.sock')process.exit(2);console.log('verification');process.exit(require('fs').existsSync('ready') ? 0 : 1)"]);
const fix = command(['-e', "if(process.argv[2]!=='false')process.exit(2);if(process.argv[1]==='true')require('fs').writeFileSync('ready','')", '', ''],
  { onNonZero: 'fail', argTemplates: [null, null, '{{repair}}', '{{passed}}'] });
const finish = command(['-e', "require('fs').writeFileSync('finished','')"]);
const compiled = workflow('command-passed', {
  input: z.object({ repair: z.boolean() }),
  build: ({ input, step, repeatUntil }) => {
    const verified = repeatUntil('verification', {
      attempt: () => step('verify', verify, {}),
      until: result => result.passed,
      otherwise: result => step('fix', fix, {
        repair: input.repair, passed: result.passed,
        failures: { exitCode: result.exitCode, stdout: result.stdout, stderr: result.stderr },
      }),
      maxAttempts: 2,
      onExhausted: 'pause',
    });
    step('finish', finish, {}, { requiresPassed: verified });
    return { passed: verified.value.passed, attempts: verified.attempts, stdout: verified.value.stdout };
  },
});
console.log(JSON.stringify(compiled.plan));`)
	compile.Dir = root
	compile.Env = append(os.Environ(), "TSX_DISABLE_CACHE=1")
	raw, err := compile.CombinedOutput()
	if err != nil {
		t.Fatalf("compile workflow: %v\n%s", err, raw)
	}
	var p plan.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, raw)
	}
	adapterDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(adapterDir, "command"), "./cmd/command")
	build.Dir = filepath.Join(root, "muxlio-adapters")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build command adapter: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name     string
		ready    bool
		repair   bool
		state    string
		verifies int
		fixes    int
	}{
		{"immediate success", true, false, plan.RunSucceeded, 1, 0},
		{"fix then success", false, true, plan.RunSucceeded, 2, 1},
		{"exhaustion pauses before finish", false, false, plan.RunPaused, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if tc.ready {
				if err := os.WriteFile(filepath.Join(workspace, "ready"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			executor := workers.NewScriptExecutor(adapterDir, filepath.Join(t.TempDir(), "work"))
			executor.Workspace = workspace
			executor.AllowCommandHost = true
			executor.SSHAgentSocket = "/test/worker-agent.sock"
			engine := plan.NewEngine(&p, executor)
			input, _ := json.Marshal(map[string]bool{"repair": tc.repair})
			id, err := engine.CreateRun(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Run(id); err != nil {
				t.Fatal(err)
			}
			run, err := engine.Store().GetRun(id)
			if err != nil || run.State != tc.state {
				t.Fatalf("run = %#v, error = %v; want %s", run, err, tc.state)
			}
			acts, err := engine.Store().ListActivations(id)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, a := range acts {
				counts[a.NodeID]++
				if a.NodeID == "verify" {
					if _, wrapped := a.Outputs["_result"]; wrapped {
						t.Fatalf("unexpected wrapper output: %#v", a.Outputs)
					}
					if a.Outputs["passed"] != (a.Outputs["exitCode"] == float64(0)) || a.Outputs["signal"] != nil {
						t.Fatalf("flat command details: %#v", a.Outputs)
					}
				}
			}
			if counts["verify"] != tc.verifies || counts["fix"] != tc.fixes {
				t.Fatalf("activation counts: %v", counts)
			}
			_, finishErr := os.Stat(filepath.Join(workspace, "finished"))
			if tc.state == plan.RunSucceeded {
				output, ok := run.Output.(map[string]any)
				if finishErr != nil || !ok || output["passed"] != true || output["attempts"] != float64(tc.verifies) || output["stdout"] != "verification\n" {
					t.Fatalf("successful output = %#v, finish error = %v", run.Output, finishErr)
				}
			} else if !os.IsNotExist(finishErr) || counts["finish"] != 0 {
				t.Fatalf("finish ran before verification passed: %v, %v", counts, finishErr)
			}
		})
	}
}
