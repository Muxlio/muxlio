//go:build unix

package authoring

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestCompileStopsDescendants(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is required for compiler process tests")
	}
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "1")
	for _, tc := range []struct {
		name, stdio, finish string
	}{
		{"success", "ignore", `export default {plan:{subgraphs:{}}};`},
		{"failure", "ignore", `throw new Error('fixture failure');`},
		{"inherited pipes", "inherit", `export default {plan:{subgraphs:{}}};`},
		{"timeout", "ignore", `await new Promise(() => setInterval(() => {}, 1000));`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runner := filepath.Join(root, "node_modules", "tsx", "dist")
			if err := os.MkdirAll(runner, 0700); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(runner, "..", "package.json"), `{"version":"`+compilerVersion+`"}`)
			write(filepath.Join(runner, "cli.mjs"), `await import('data:text/javascript,' + encodeURIComponent(process.argv[3]));`)
			heartbeat := filepath.Join(root, "heartbeat")
			pidPath := filepath.Join(root, "pid")
			heartbeatJSON, _ := json.Marshal(heartbeat)
			pidJSON, _ := json.Marshal(pidPath)
			child := fmt.Sprintf(`const fs = require('node:fs'); let count = 0; const beat = () => fs.writeFileSync(%s, String(++count)); beat(); setInterval(beat, 20); process.send('ready'); process.disconnect();`, heartbeatJSON)
			childJSON, _ := json.Marshal(child)
			source := filepath.Join(root, "workflow.mjs")
			write(source, fmt.Sprintf(`
import {spawn} from 'node:child_process';
import fs from 'node:fs';
const child = spawn(process.execPath, ['-e', %s], {stdio:['ignore', %q, %q, 'ipc']});
fs.writeFileSync(%s, String(child.pid));
await new Promise(resolve => child.once('message', resolve));
child.unref();
%s
`, childJSON, tc.stdio, tc.stdio, pidJSON, tc.finish))
			// Clean up even when the regression is present.
			t.Cleanup(func() {
				raw, _ := os.ReadFile(pidPath)
				if pid, err := strconv.Atoi(string(raw)); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			_, err := compile(source, 2*time.Second)
			if tc.name == "success" && err != nil {
				t.Fatal(err)
			}
			if tc.name != "success" && err == nil {
				t.Fatal("expected compilation error")
			}
			// A heartbeat avoids mistaking an exited but unreaped Unix zombie
			// for a running descendant when checking process existence.
			time.Sleep(100 * time.Millisecond)
			before, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(150 * time.Millisecond)
			after, err := os.ReadFile(heartbeat)
			if err != nil || string(before) != string(after) {
				t.Fatalf("compiler descendant still running: heartbeat %q -> %q (%v)", before, after, err)
			}
		})
	}
}
