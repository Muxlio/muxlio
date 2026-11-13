package adapter

import (
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

// QualifyRunnerChecks applies adapter-owned CLI compatibility and login guidance.
// Raw command output is never included in readiness diagnostics.
func QualifyRunnerChecks(checks []contract.ProbeCheck, qualify func(string) error, login string) []contract.ProbeCheck {
	for i := range checks {
		c := &checks[i]
		if c.Name == contract.ProbeVersion && c.Passed {
			if err := qualify(c.Version); err != nil {
				c.Passed = false
				c.Message = err.Error()
			}
		}
		if c.Name == contract.ProbeAuthentication && !c.Passed {
			c.Message = "login verification failed; run " + login + " with the executor's environment and credential store, then retry readiness"
		}
	}
	return checks
}

// RunnerFailure adds actionable guidance while leaving accounting untouched.
// The adapter supplies its provider-specific login command.
func RunnerFailure(reason, diagnostics, login string) string {
	text := strings.ToLower(reason + " " + diagnostics)
	for _, marker := range []string{"unknown option", "unknown flag", "unexpected argument", "unrecognized option", "flag provided but not defined"} {
		if strings.Contains(text, marker) {
			return reason + "; CLI rejected an invocation flag: verify the runner version with --version, install an adapter-qualified build or update the adapter, then retry readiness"
		}
	}
	for _, marker := range []string{"not logged in", "unauthenticated", "auth failed", "authentication failed", "login required", "permission denied", "permission prompt", "approval required"} {
		if strings.Contains(text, marker) {
			return reason + "; verify login with " + login + " in the executor's environment and review the operator authorization policy before retrying"
		}
	}
	return reason
}
