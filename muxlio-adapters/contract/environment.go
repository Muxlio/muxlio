package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

// EnvironmentManifest records operator-managed software and permissions, never
// environment values or credentials. Exact pins describe compatibility, not
// deterministic model output. Distribution and installation remain operator-owned.
type EnvironmentManifest struct {
	Version            string            `json:"version"`
	Digest             string            `json:"digest"`
	ProtocolDigest     string            `json:"protocolDigest"`
	Runners            map[string]string `json:"runners,omitempty"`
	Packages           map[string]string `json:"packages,omitempty"`
	Containers         map[string]string `json:"containers,omitempty"`
	Skills             map[string]string `json:"skills,omitempty"`
	AllowedEnvironment []string          `json:"allowedEnvironment,omitempty"`
	Permissions        []string          `json:"permissions,omitempty"`
}

// Reject extra fields rather than silently retaining credentials in source JSON.
func (m *EnvironmentManifest) UnmarshalJSON(raw []byte) error {
	type plain EnvironmentManifest
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid environment manifest")
	}
	*m = EnvironmentManifest(value)
	return nil
}

var environmentDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_. /-]{0,127}$`)
var environmentVariable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var runnerVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$`)

// IsRunnerVersion reports whether a complete normalized version is supported.
func IsRunnerVersion(version string) bool { return runnerVersion.MatchString(version) }

// ComputeDigest hashes only public, typed metadata in stable JSON field order.
func (m *EnvironmentManifest) ComputeDigest() string {
	copy := *m
	copy.Digest = ""
	raw, _ := json.Marshal(copy)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (m *EnvironmentManifest) Validate() error {
	if m == nil {
		return nil
	}
	if m.Version != "1" || !environmentDigest.MatchString(m.ProtocolDigest) || !environmentDigest.MatchString(m.Digest) {
		return fmt.Errorf("environment manifest requires version 1 and SHA-256 protocol and manifest digests")
	}
	for name, version := range m.Runners {
		if !environmentName.MatchString(name) || !runnerVersion.MatchString(version) {
			return fmt.Errorf("invalid environment runner pin")
		}
	}
	for _, pins := range []map[string]string{m.Packages, m.Containers, m.Skills} {
		for name, digest := range pins {
			if !environmentName.MatchString(name) || !environmentDigest.MatchString(digest) {
				return fmt.Errorf("invalid environment content pin")
			}
		}
	}
	for _, name := range m.AllowedEnvironment {
		if !environmentVariable.MatchString(name) {
			return fmt.Errorf("allowed environment contains names only")
		}
	}
	for _, permission := range m.Permissions {
		switch permission {
		case "host-execution", "network", "workspace-read", "workspace-write":
		default:
			return fmt.Errorf("unknown environment permission")
		}
	}
	if m.ComputeDigest() != m.Digest {
		return fmt.Errorf("environment manifest digest mismatch")
	}
	return nil
}

// CheckEnvironment fails closed on missing or changed software/permission evidence.
func CheckEnvironment(required, available *EnvironmentManifest) error {
	if required == nil {
		return nil
	}
	if err := required.Validate(); err != nil {
		return err
	}
	if available == nil {
		return fmt.Errorf("pinned environment unavailable")
	}
	if err := available.Validate(); err != nil {
		return err
	}
	if required.Digest != available.Digest {
		return fmt.Errorf("incompatible pinned environment")
	}
	return nil
}

func (m *EnvironmentManifest) CheckRunners(actual map[string]string) error {
	if m == nil {
		return nil
	}
	for name, version := range m.Runners {
		if actual[name] != version {
			return fmt.Errorf("pinned runner version unavailable")
		}
	}
	return nil
}

// FilterEnvironment enforces the pinned allowlist without serializing values.
func (m *EnvironmentManifest) FilterEnvironment(env []string) []string {
	if m == nil {
		return env
	}
	allowed := make(map[string]bool)
	for _, name := range m.AllowedEnvironment {
		allowed[name] = true
	}
	// A non-nil empty slice is required: exec.Cmd.Env == nil inherits the host.
	result := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := bytes.Cut([]byte(entry), []byte("="))
		if ok && allowed[string(name)] {
			result = append(result, entry)
		}
	}
	return result
}

// ProtocolDigest addresses the adapter's public descriptor independently of the
// operator's environment, so embedding a manifest never creates a hash cycle.
func (m *AdapterManifest) ProtocolDigest() string {
	copy := *m
	copy.Environment = nil
	raw, _ := json.Marshal(copy)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (m *AdapterManifest) ValidateEnvironment() error {
	if m.Environment == nil {
		return nil
	}
	if err := m.Environment.Validate(); err != nil {
		return err
	}
	if m.Environment.ProtocolDigest != m.ProtocolDigest() {
		return fmt.Errorf("environment adapter protocol digest mismatch")
	}
	return nil
}
