package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestArgvIncludesJSONAfterSubcommand(t *testing.T) {
	raw := []string{"check", "/tmp/plan.ts", "--json"}
	if !argvIncludesJSON(raw) {
		t.Fatal("expected trailing --json to be recognized")
	}
	if argvIncludesJSON([]string{"check", "/tmp/plan.ts"}) {
		t.Fatal("expected absent --json")
	}
}

func TestPartitionArgsSkipsFlagValuesBeforeCommand(t *testing.T) {
	before, rest := partitionArgs([]string{"--server", "http://127.0.0.1:7600", "status", "run-1"})
	if len(before) != 2 || before[1] != "http://127.0.0.1:7600" {
		t.Fatalf("before = %v", before)
	}
	want := []string{"status", "run-1"}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("rest = %v, want %v", rest, want)
	}
}

func TestReorderArgsBooleanFlagsDoNotConsumePositionals(t *testing.T) {
	got := reorderArgs([]string{"/workspace", "--allow-command"})
	want := []string{"--allow-command", "/workspace"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorderArgs() = %v, want %v", got, want)
	}
}

func TestReorderArgsConfigOnlyDoesNotConsumeNextArgument(t *testing.T) {
	got := reorderArgs([]string{"doctor", "--config-only", "extra"})
	want := []string{"--config-only", "doctor", "extra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorderArgs() = %v, want %v", got, want)
	}
}

func TestWorkerSubcommandHonorsAdaptersAndDataDir(t *testing.T) {
	if os.Getenv("MUXLIO_TEST_CLI") == "1" {
		return
	}
	root := t.TempDir()
	adapters := filepath.Join(root, "custom-adapters")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIProcess$", "--", "worker", "status", "--adapters", adapters, "--data-dir", root)
	cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), adapters) {
		t.Fatalf("worker status ignored --adapters: %s", out)
	}
}

func TestForwardRemoteFlags(t *testing.T) {
	got := forwardRemoteFlags([]string{"run-id"}, "http://coordinator:7600", "tok", "")
	want := []string{"--server", "http://coordinator:7600", "--token", "tok", "run-id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forwardRemoteFlags() = %v, want %v", got, want)
	}
}

func TestPartitionArgsPreservesEqualsFormGlobals(t *testing.T) {
	before, rest := partitionArgs([]string{"--server=http://127.0.0.1:7600", "status", "run-1"})
	wantBefore := []string{"--server=http://127.0.0.1:7600"}
	wantRest := []string{"status", "run-1"}
	if !reflect.DeepEqual(before, wantBefore) {
		t.Fatalf("before = %v, want %v", before, wantBefore)
	}
	if !reflect.DeepEqual(rest, wantRest) {
		t.Fatalf("rest = %v, want %v", rest, wantRest)
	}
}

func TestAbsorbFlagValueEqualsForm(t *testing.T) {
	args, val := absorbFlagValue("server", []string{"plan.json", "--server=http://example:7600"}, "")
	if val != "http://example:7600" {
		t.Fatalf("server = %q", val)
	}
	want := []string{"plan.json"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}
