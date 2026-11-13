package authoring

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
)

func TestTemplatesValidateAndEchoPreservesInput(t *testing.T) {
	t.Setenv("PATH", "")
	for _, name := range []string{"echo", "task"} {
		raw, err := Template(name)
		if err != nil {
			t.Fatal(err)
		}
		var p plan.Plan
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if issues := plan.Validate(&p); len(issues) != 0 {
			t.Fatalf("%s: %+v", name, issues)
		}
		if name == "echo" {
			e := plan.NewEngine(&p, nil)
			id, err := e.CreateRun(json.RawMessage(`{"message":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Run(id); err != nil {
				t.Fatal(err)
			}
			run, _ := e.Store().GetRun(id)
			output, _ := json.Marshal(run.Output)
			if run.State != "succeeded" || !strings.Contains(string(output), `"hello"`) {
				t.Fatalf("echo: %+v", run)
			}
		}
	}
	if _, err := Template("../echo"); err == nil {
		t.Fatal("accepted unknown template")
	}
}

func TestCompileRequiresTrustAndNeverFetchesPackages(t *testing.T) {
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "")
	t.Setenv("PATH", "")
	path := filepath.Join(t.TempDir(), "workflow.ts")
	if _, err := Compile(path); err == nil || !strings.Contains(err.Error(), "trusted definitions") {
		t.Fatalf("trust guard: %v", err)
	}
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "1")
	if _, err := Compile(path); err == nil || !strings.Contains(err.Error(), "no project-local tsx") {
		t.Fatalf("offline failure: %v", err)
	}
}

func TestTrustedCompileUsesNearestProjectRunner(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is needed only for optional source compilation")
	}
	root := t.TempDir()
	runner := filepath.Join(root, "node_modules", "tsx", "dist")
	if err := os.MkdirAll(runner, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(root, "node_modules", "tsx", "package.json")
	if err := os.WriteFile(metadata, []byte(`{"version":"`+compilerVersion+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// A local runner fixture checks the invocation contract without downloading tsx.
	source := `import { pathToFileURL } from 'node:url'; if (process.env.MUXLIO_COMPILE !== '1') process.exit(2); await import('data:text/javascript,' + encodeURIComponent(process.argv[3]));`
	if err := os.WriteFile(filepath.Join(runner, "cli.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "unrelated workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "workflow.mjs")
	if err := os.WriteFile(path, []byte(`export default {plan:{subgraphs:{},name:'local'}};`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(bin, "node")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "1")
	raw, err := Compile(path)
	if err != nil || !strings.Contains(string(raw), `"name":"local"`) {
		t.Fatalf("compile: %s, %v", raw, err)
	}

	// Verify relative paths resolve deterministically up to parent runner.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	relRaw, err := Compile("workflow.mjs")
	if err != nil || !strings.Contains(string(relRaw), `"name":"local"`) {
		t.Fatalf("compile relative path: %s, %v", relRaw, err)
	}

	t.Setenv("MUXLIO_TOKEN", "operator-secret")
	t.Setenv("NODE_OPTIONS", "--require=/nonexistent/operator-hook.js")
	originalHome := os.Getenv("HOME")
	writeSource := func(source string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource(`export default {plan:{subgraphs:{},env:process.env}};`)
	raw, err = Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Env["MUXLIO_TOKEN"] != "" || result.Env["HOME"] == originalHome || result.Env["NODE_OPTIONS"] != "--max-old-space-size=256" {
		t.Fatalf("unsafe compiler environment: %s", raw)
	}
	if _, err := os.Stat(result.Env["HOME"]); !os.IsNotExist(err) {
		t.Fatalf("compiler temporary home not removed: %v", err)
	}

	writeSource(`await new Promise(() => { setInterval(() => {}, 1000); });`)
	started := time.Now()
	if _, err := compile(path, 200*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("compiler did not stop promptly")
	}
	for _, stream := range []string{"stdout", "stderr"} {
		writeSource(`process.` + stream + `.write('x'.repeat(9 * 1024 * 1024)); setInterval(() => {}, 1000);`)
		if _, err := Compile(path); err == nil || !strings.Contains(err.Error(), "output limit") {
			t.Fatalf("%s limit: %v", stream, err)
		}
	}
	if err := os.WriteFile(metadata, []byte(`{"version":"0.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(path); err == nil || !strings.Contains(err.Error(), "requires project-local tsx "+compilerVersion) {
		t.Fatalf("compiler version guard: %v", err)
	}
}
