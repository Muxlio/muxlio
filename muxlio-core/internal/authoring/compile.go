package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const compilerVersion = "4.23.15"
const compileTimeout = 30 * time.Second
const maxPlanBytes = 8 << 20
const maxDiagnosticBytes = 64 << 10

// Compile executes arbitrary local code. Never invoke package managers here:
// resolution is restricted to an explicitly installed project-local tsx.
func Compile(filePath string) ([]byte, error) {
	return compile(filePath, compileTimeout)
}

func compile(filePath string, timeout time.Duration) ([]byte, error) {
	if os.Getenv("MUXLIO_TRUST_WORKFLOW_CODE") != "1" {
		return nil, fmt.Errorf("TypeScript/JavaScript compilation executes arbitrary code; only for trusted definitions, set MUXLIO_TRUST_WORKFLOW_CODE=1. Use muxlio template echo or task for offline JSON authoring")
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	var runner string
	for current := dir; ; current = filepath.Dir(current) {
		candidate := filepath.Join(current, "node_modules", "tsx", "dist", "cli.mjs")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			metadata, err := os.ReadFile(filepath.Join(current, "node_modules", "tsx", "package.json"))
			var pkg struct {
				Version string `json:"version"`
			}
			if err != nil || json.Unmarshal(metadata, &pkg) != nil || pkg.Version != compilerVersion {
				return nil, fmt.Errorf("source compilation requires project-local tsx %s; install the pinned lockfile explicitly", compilerVersion)
			}
			runner = candidate
			break
		}
		if filepath.Dir(current) == current {
			return nil, fmt.Errorf("no project-local tsx installed; install dependencies explicitly using your project's pinned lockfile, or use a built-in JSON template (no packages required)")
		}
	}
	snippet := fmt.Sprintf(`
import { pathToFileURL } from 'node:url';
import(pathToFileURL(%q).href).then((mod) => {
  const def = mod.default?.default || mod.default || mod;
  const plan = def?.plan || (def?.subgraphs ? def : null);
  if (!plan || !plan.subgraphs) throw new Error('workflow must export a plan');
  process.stdout.write(JSON.stringify(plan));
}).catch((err) => { console.error(err); process.exitCode = 1; });
`, abs)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	home, err := os.MkdirTemp("", "muxlio-compile-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	cmd := exec.CommandContext(ctx, "node", runner, "-e", snippet)
	cmd.Dir = dir
	// Do not forward operator credentials, Node hooks, or package-manager settings.
	// This is defense in depth for trusted code, not filesystem/network isolation.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + home,
		"MUXLIO_COMPILE=1", "NODE_OPTIONS=--max-old-space-size=256", "TSX_DISABLE_CACHE=1"}
	cmd.WaitDelay = time.Second
	cleanupProcess := boundProcess(cmd)
	defer cleanupProcess()
	stdout := boundedOutput{limit: maxPlanBytes, cancel: cancel}
	stderr := boundedOutput{limit: maxDiagnosticBytes, cancel: cancel}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("workflow compilation exceeded output limit (plan %d bytes, diagnostics %d bytes)", maxPlanBytes, maxDiagnosticBytes)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("workflow compilation timed out after %s", timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("could not compile workflow %s: %v: %s", filePath, err, strings.TrimSpace(stderr.buffer.String()))
	}
	if stdout.buffer.Len() == 0 {
		return nil, fmt.Errorf("workflow %s produced no plan", filePath)
	}
	return stdout.buffer.Bytes(), nil
}

// Retain bounded output and stop execution as soon as either stream exceeds it.
type boundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if n > remaining {
		p = p[:remaining]
		b.exceeded = true
		b.cancel()
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
