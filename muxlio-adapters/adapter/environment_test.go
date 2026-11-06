package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestChangedLocalRunnerInvalidatesPinnedEnvironment(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "runner")
	write := func(version string) {
		t.Helper()
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' 'runner "+version+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("1.2.3")
	req := &Request{Environment: &contract.EnvironmentManifest{Runners: map[string]string{"runner": "1.2.3"}}}
	if err := CheckRunnerEnvironment(req, binary); err != nil {
		t.Fatal(err)
	}
	write("2.0.0")
	if err := CheckRunnerEnvironment(req, binary); err == nil {
		t.Fatal("changed local CLI accepted pinned plan")
	}
}

func TestRunnerVersionChecksPreserveCompletePins(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "runner")
	for _, tc := range []struct{ output, expected string }{
		{"runner v1.2.3-beta+build.1", "1.2.3-beta+build.1"},
		{"runner 1.2.3.4", ""},
		{"runner 1.2.3-beta+build.2", "1.2.3-beta+build.2"},
	} {
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.output+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		check := CommandCheck(contract.ProbeVersion, binary, "--version")
		if check.Version != tc.expected || check.Passed != (tc.expected != "") {
			t.Fatalf("%q: %+v", tc.output, check)
		}
		req := &Request{Environment: &contract.EnvironmentManifest{Runners: map[string]string{"runner": "1.2.3-beta+build.1"}}}
		err := CheckRunnerEnvironment(req, binary)
		if (err == nil) != (tc.expected == "1.2.3-beta+build.1") {
			t.Fatalf("%q: %v", tc.output, err)
		}
	}
}
