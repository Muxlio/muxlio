package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Probe check categories. These are provider-independent labels: the adapter
// performs the provider-specific verification for each it supports, and the
// worker performs the operator-owned ones. A readiness report never branches
// on executor names; it is a generic list of named checks.
const (
	// ProbeProtocol reports contract/manifest compatibility. Verified by the
	// worker from the adapter descriptor, so it does not require executing the
	// adapter binary.
	ProbeProtocol = "protocol"
	// ProbeBinary reports that the provider executable/CLI is installed and
	// runnable. Verified by the adapter, which owns provider resolution.
	ProbeBinary = "binary"
	// ProbeVersion reports that the provider runner version is known and
	// available. Verified by the adapter.
	ProbeVersion = "version"
	// ProbePermission reports that operator policy authorizes the executor.
	// Verified by the worker from its local executor configuration.
	ProbePermission = "permission"
	// ProbeAuthentication reports that provider authentication is available
	// (e.g. the CLI is logged in). Verified by the adapter; the worker never
	// inspects provider credentials.
	ProbeAuthentication = "authentication"
	// ProbeBackend reports that a required execution environment backend
	// is available. Adapters verify their own backend requirements; worker
	// isolation support additionally requires enforced backend labels.
	ProbeBackend = "backend"
	// ProbeTools reports that any companion tools the adapter depends on are
	// available. Verified by the adapter.
	ProbeTools = "tools"
)

// ProbeCheck is one non-destructive readiness verification. Message is a
// human-readable explanation that must never contain secrets, credentials,
// or tokens. It is safe to surface in diagnostics, doctor output, and logs.
type ProbeCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

// ProbeResult is the structured readiness report an adapter emits when invoked
// in probe mode (the worker passes the --muxlio-probe flag). Ready is true
// only when every named check passed. An adapter without probe support is unverified and must not be advertised.
type ProbeResult struct {
	Ready  bool         `json:"ready"`
	Checks []ProbeCheck `json:"checks,omitempty"`
}

// Add appends a check and updates Ready. Ready starts true; any failed check
// makes the overall result not ready.
func (r *ProbeResult) Add(c ProbeCheck) {
	if r == nil {
		return
	}
	r.Checks = append(r.Checks, c)
	if !c.Passed {
		r.Ready = false
	}
}

// Failed returns the checks that did not pass, preserving order.
func (r *ProbeResult) Failed() []ProbeCheck {
	if r == nil {
		return nil
	}
	var out []ProbeCheck
	for _, c := range r.Checks {
		if !c.Passed {
			out = append(out, c)
		}
	}
	return out
}

// ParseProbeResult fails closed on missing evidence or incompatible protocols.
func ParseProbeResult(raw []byte) (ProbeResult, error) {
	var result ProbeResult
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return result, fmt.Errorf("probe result must be a JSON object")
	}
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return result, fmt.Errorf("invalid probe result")
	}
	if len(result.Checks) == 0 {
		return result, fmt.Errorf("probe result requires checks")
	}
	for _, c := range result.Checks {
		if c.Name == "" {
			return ProbeResult{}, fmt.Errorf("probe check requires name")
		}
		if !c.Passed {
			result.Ready = false
		}
	}
	return result, nil
}
