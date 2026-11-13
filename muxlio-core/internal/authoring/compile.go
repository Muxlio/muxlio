package authoring

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Compile executes arbitrary local code. Never invoke package managers here:
// resolution is restricted to an explicitly installed project-local tsx.
func Compile(filePath string) ([]byte, error) {
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
	cmd := exec.Command("node", runner, "-e", snippet)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MUXLIO_COMPILE=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("could not compile workflow %s: %v: %s", filePath, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("workflow %s produced no plan", filePath)
	}
	return stdout.Bytes(), nil
}
