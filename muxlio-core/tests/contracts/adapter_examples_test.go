package contracts_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAdapterExampleWorkflowsFakeDryRun(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "muxlio-sdk/dist/index.js")); err != nil {
		t.Skip("run npm run build first")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "muxlio")
	build := exec.Command("go", "build", "-o", cli, "./cmd/muxlio")
	build.Dir = filepath.Join(root, "muxlio-core")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	call := func(t *testing.T, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "MUXLIO_HOME="+dir, "MUXLIO_CONFIG="+filepath.Join(dir, "config.json"), "MUXLIO_SERVER=", "TSX_DISABLE_CACHE=1", "TMPDIR=/tmp")
		out, err := cmd.Output()
		if err != nil {
			if failure, ok := err.(*exec.ExitError); ok {
				t.Fatalf("muxlio %v: %v\nstdout: %s\nstderr: %s", args, err, out, failure.Stderr)
			}
			t.Fatalf("muxlio %v: %v\n%s", args, err, out)
		}
		if !json.Valid(out) {
			t.Fatalf("muxlio %v returned invalid JSON: %s", args, out)
		}
		return out
	}

	for _, name := range []string{"acp", "a2a"} {
		workflow := filepath.Join(root, "examples", name+".workflow.ts")
		input := filepath.Join(root, "examples", name+".input.json")
		fake := filepath.Join(root, "examples", name+".fake.json")
		call(t, "check", workflow, "--json")
		compiled := filepath.Join(dir, name+".plan.json")
		call(t, "build", workflow, "--out", compiled, "--json")
		call(t, "test", compiled, "--input", input, "--fake-config", fake, "--json")
	}
}
