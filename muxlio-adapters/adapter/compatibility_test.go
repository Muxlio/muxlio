package adapter

import (
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestReadinessPreservesUnpinnedVersions(t *testing.T) {
	for _, version := range []string{"1.0.0", "2.0.0"} {
		checks := []contract.ProbeCheck{
			{Name: contract.ProbeVersion, Passed: true, Runner: "runner", Version: version},
			{Name: contract.ProbeAuthentication, Passed: false},
		}
		checks = RunnerLoginGuidance(checks, "example login")
		if !checks[0].Passed || checks[0].Version != version {
			t.Fatalf("version evidence changed: %+v", checks)
		}
		if !strings.Contains(checks[1].Message, "example login") {
			t.Fatal(checks)
		}
	}
}

func TestRunnerFailureGuidance(t *testing.T) {
	for _, tc := range []struct{ diagnostic, want string }{
		{"unknown option --headless", "compatible"},
		{"unexpected argument --permission", "compatible"},
		{"not logged in", "example login"},
		{"authentication failed", "example login"},
		{"permission denied", "operator authorization"},
		{"approval required", "operator authorization"},
		{"network timeout", ""},
	} {
		got := RunnerFailure("execution failed", tc.diagnostic, "example login")
		if tc.want == "" {
			if got != "execution failed" {
				t.Fatal(got)
			}
		} else if !strings.Contains(got, tc.want) {
			t.Fatal(got)
		}
	}
}
