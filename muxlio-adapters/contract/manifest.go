// The manifest defines the versioned optional capability
// contract for adapter binaries.
//
// # Adapter manifest
//
// An adapter binary may ship a JSON descriptor at `<binary>.json` beside the
// executable. When the file exists the coordinator reads it to determine which
// optional capabilities the adapter supports before dispatching a request.
//
// # Design invariants
//
//   - Capabilities are opt-in: absence of a capability means "not supported",
//     not "unknown". An executor that lacks a capability must not silently
//     emulate it.
//   - The manifest version is separate from the executor capability version
//     declared in WorkerCapability.Version. ManifestVersion identifies the
//     schema of the JSON descriptor; ExecutorVersion identifies the adapter
//     implementation revision.
//   - Missing usage is nil; a reported zero is a non-nil pointer with all
//     fields zero. This is an invariant of the whole system, not just the
//     manifest, but it is documented here because it applies to every adapter
//     capability that carries usage.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ManifestVersion is the current schema version for adapter descriptor files.
const ManifestVersion = "1"

// Capability names for optional adapter capabilities.
const (
	// CapabilityCancel indicates the adapter supports receiving a cancellation
	// signal mid-execution and terminates cleanly. Without this capability the
	// coordinator must rely on process-level termination only.
	CapabilityCancel = "cancel"

	// CapabilityProgress indicates the adapter streams incremental progress
	// events over the live log channel (MUXLIO_LOG_FD) during execution.
	CapabilityProgress = "progress"

	// CapabilityArtifacts indicates the adapter can produce file-artifact
	// output ports (OutputDecl.Path != ""). Adapters without this capability
	// must only return scalar outputs.
	CapabilityArtifacts = "artifacts"
	// CapabilityTypedResult guarantees native typed generation. Consumer-side
	// validation of OutputDecl.Schema alone does not imply this capability.
	CapabilityTypedResult = "typedResult"
	CapabilityUsage       = "usage"

	// CapabilitySession indicates the adapter supports resuming an existing
	// provider session across invocations using a stable session identifier
	// carried in Request.SessionID (legacy Params.sessionId is also checked).
	CapabilitySession = "session"

	// CapabilityRemoteTask indicates the adapter can submit work to a remote
	// provider task queue and return a task reference for later polling, rather
	// than blocking until completion.
	CapabilityRemoteTask = "remoteTask"
)

// AdapterManifest is the contents of an adapter's optional JSON descriptor
// (`<adapter-name>.json` beside the binary). Workers read this file to
// determine capabilities before dispatch; the coordinator uses it to verify
// compatibility.
//
// All fields are optional. A minimal descriptor may contain only the executor
// name and leave all capability flags false (absent), which is equivalent to a
// missing descriptor: every optional capability is unsupported.
type AdapterManifest struct {
	// ManifestVersion is the schema version of this descriptor. Must be "1"
	// for the current schema; unknown versions must be rejected.
	ManifestVersion string `json:"manifestVersion,omitempty"`

	// Executor is the canonical capability name the adapter handles,
	// e.g. "agent/claude". Must match the executor name used in plans.
	Executor string `json:"executor,omitempty"`

	// ExecutorVersion is the semantic version of this adapter implementation.
	// Matches the WorkerCapability.Version the worker advertises.
	ExecutorVersion string `json:"executorVersion,omitempty"`

	// RequiresHostExecution is true when the adapter requires access to the
	// host environment (native credential stores, GUI keychain, etc.) and must
	// not run inside an isolated sandbox. Workers that have not explicitly
	// allowed host execution must refuse these adapters.
	RequiresHostExecution bool `json:"requiresHostExecution,omitempty"`

	// Capabilities is the set of optional capabilities this adapter supports.
	// Keys are capability name constants (CapabilityCancel, etc.); a true
	// value declares support. A missing or false value means unsupported.
	Capabilities map[string]bool `json:"capabilities,omitempty"`
	// CapabilityVersions overrides the default version 1 for declared capabilities.
	CapabilityVersions map[string]string `json:"capabilityVersions,omitempty"`
	ContractVersion    string            `json:"contractVersion,omitempty"`
}

// Supports reports whether the manifest declares support for the named
// capability. A nil receiver (missing descriptor) reports false for all
// capabilities.
func (m *AdapterManifest) Supports(capability string) bool {
	if m == nil {
		return false
	}
	return m.Capabilities[capability]
}

// UnsupportedCapabilityError is returned when a request requires a capability
// that the adapter's manifest does not declare. Workers must surface this as
// an executor-level error so the coordinator can report the mismatch explicitly
// rather than silently emulating or ignoring the requirement.
type UnsupportedCapabilityError struct {
	Capability string
	Executor   string
}

func (e *UnsupportedCapabilityError) Error() string {
	if e.Executor != "" {
		return fmt.Sprintf("executor %q does not support capability %q", e.Executor, e.Capability)
	}
	return fmt.Sprintf("adapter does not support capability %q", e.Capability)
}

// ParseManifest parses a raw adapter descriptor JSON blob. Returns a default
// (no capabilities) manifest and a nil error when raw is nil or empty, so
// callers without a descriptor get a safe zero-value manifest. Returns an
// error only when the JSON is structurally invalid or the manifest version is
// unrecognised.
func ParseManifest(raw []byte) (*AdapterManifest, error) {
	if len(raw) == 0 {
		return &AdapterManifest{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("adapter manifest: expected object")
	}
	var m AdapterManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("adapter manifest: invalid JSON: %w", err)
	}
	if m.ManifestVersion != "" && m.ManifestVersion != ManifestVersion {
		return nil, fmt.Errorf("adapter manifest: unsupported version %q (expected %q)", m.ManifestVersion, ManifestVersion)
	}
	if m.ContractVersion != "" && m.ContractVersion != Version {
		return nil, fmt.Errorf("adapter contract: unsupported version %q", m.ContractVersion)
	}
	return &m, nil
}

// Check verifies compatibility before an invocation starts. Version requirements
// are exact: support for an unknown revision must never be inferred.
func (m *AdapterManifest) Check(executor, executorVersion, contractVersion string, required map[string]string) error {
	if m == nil {
		m = &AdapterManifest{}
	}
	if m.ManifestVersion != "" && m.ManifestVersion != ManifestVersion {
		return fmt.Errorf("adapter manifest: unsupported version %q", m.ManifestVersion)
	}
	if m.ContractVersion != "" && m.ContractVersion != Version {
		return fmt.Errorf("adapter contract: unsupported version %q", m.ContractVersion)
	}
	if m.Executor != "" && m.Executor != executor {
		return fmt.Errorf("adapter executor %q does not match %q", m.Executor, executor)
	}
	if executorVersion != "" && executorVersion != m.ExecutorVersion {
		return fmt.Errorf("executor %q version %q unavailable (adapter %q)", executor, executorVersion, m.ExecutorVersion)
	}
	if contractVersion != "" && (contractVersion != Version || m.ContractVersion != contractVersion) {
		return fmt.Errorf("executor %q does not support contract version %q", executor, contractVersion)
	}
	for name, version := range required {
		if !m.Supports(name) {
			return &UnsupportedCapabilityError{Executor: executor, Capability: name}
		}
		actual := m.CapabilityVersions[name]
		if actual == "" {
			actual = Version
		}
		if version != "" && version != actual {
			return fmt.Errorf("executor %q does not support capability %q version %q (adapter %q)", executor, name, version, actual)
		}
	}
	return nil
}
