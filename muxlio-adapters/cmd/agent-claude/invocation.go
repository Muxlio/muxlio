package main

import (
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

// An omitted model preserves the operator's CLI configuration and native selection.
func invocationArgs(p *contract.AuthorizationPolicy, version, model, scratch, prompt string) ([]string, error) {
	policyArgs, err := permissionArgs(p, version)
	if err != nil {
		return nil, err
	}
	args := []string{"--add-dir", scratch, "--print", "--verbose", "--output-format", "stream-json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(policyArgs, args...)
	args = append(args, "--", prompt)
	return args, nil
}

func runnerChecks(binary string, authArgs ...string) []contract.ProbeCheck {
	return adapter.QualifyRunnerChecks(adapter.RunnerChecks(binary, authArgs...), supportedVersion, "claude auth login")
}
