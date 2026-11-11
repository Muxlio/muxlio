package main

import (
	"reflect"
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
