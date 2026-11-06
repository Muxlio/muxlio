package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func pinnedEnvironment() *EnvironmentManifest {
	m := &EnvironmentManifest{Version: "1", ProtocolDigest: "sha256:" + strings.Repeat("a", 64), Runners: map[string]string{"runner": "1.2.3"}, Packages: map[string]string{"tool": "sha256:" + strings.Repeat("b", 64)}, Containers: map[string]string{"sandbox": "sha256:" + strings.Repeat("c", 64)}, Skills: map[string]string{"review": "sha256:" + strings.Repeat("d", 64)}, AllowedEnvironment: []string{"PATH", "API_KEY"}, Permissions: []string{"workspace-read"}}
	m.Digest = m.ComputeDigest()
	return m
}

func TestEnvironmentPinsFailClosed(t *testing.T) {
	required := pinnedEnvironment()
	if err := CheckEnvironment(required, required); err != nil {
		t.Fatal(err)
	}
	if err := CheckEnvironment(required, nil); err == nil {
		t.Fatal("missing evidence accepted")
	}
	for _, change := range []func(*EnvironmentManifest){
		func(m *EnvironmentManifest) { m.Runners["runner"] = "2.0.0" },
		func(m *EnvironmentManifest) { delete(m.Packages, "tool") },
		func(m *EnvironmentManifest) { delete(m.Containers, "sandbox") },
		func(m *EnvironmentManifest) { delete(m.Skills, "review") },
		func(m *EnvironmentManifest) { m.AllowedEnvironment = append(m.AllowedEnvironment, "HOME") },
		func(m *EnvironmentManifest) { m.Permissions = append(m.Permissions, "network") },
	} {
		available := pinnedEnvironment()
		change(available)
		if err := available.Validate(); err == nil {
			t.Fatal("mutation did not invalidate digest")
		}
		available.Digest = available.ComputeDigest()
		if err := CheckEnvironment(required, available); err == nil {
			t.Fatal("changed requirement accepted")
		}
	}
	if err := required.CheckRunners(map[string]string{"runner": "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if err := required.CheckRunners(map[string]string{"runner": "1.2.4"}); err == nil {
		t.Fatal("runner drift accepted")
	}
}

func TestEnvironmentExportsNamesNeverValues(t *testing.T) {
	t.Setenv("API_KEY", "private-credential")
	m := pinnedEnvironment()
	raw, err := json.Marshal(m)
	if err != nil || strings.Contains(string(raw), "private-credential") {
		t.Fatalf("unsafe export: %v", err)
	}
	for _, field := range []string{`"secrets":{"API_KEY":"private-credential"}`, `"values":{"API_KEY":"private-credential"}`, `"allowedEnvironment":{"API_KEY":"private-credential"}`} {
		var manifest EnvironmentManifest
		if json.Unmarshal([]byte(`{`+field+`}`), &manifest) == nil {
			t.Fatal("credential field accepted")
		}
	}
	m.AllowedEnvironment = []string{"API_KEY=private-credential"}
	m.Digest = m.ComputeDigest()
	if m.Validate() == nil {
		t.Fatal("environment value accepted")
	}
	m = pinnedEnvironment()
	got := m.FilterEnvironment([]string{"PATH=/bin", "API_KEY=private-credential", "EXTRA=private-extra"})
	if len(got) != 2 || got[1] != "API_KEY=private-credential" {
		t.Fatalf("allowlist not enforced: %v", got)
	}
}

func TestEnvironmentProtocolBinding(t *testing.T) {
	descriptor := &AdapterManifest{Executor: "test", ContractVersion: Version}
	m := pinnedEnvironment()
	m.ProtocolDigest = descriptor.ProtocolDigest()
	m.Digest = m.ComputeDigest()
	descriptor.Environment = m
	if err := descriptor.ValidateEnvironment(); err != nil {
		t.Fatal(err)
	}
	descriptor.ExecutorVersion = "changed"
	if err := descriptor.ValidateEnvironment(); err == nil {
		t.Fatal("changed protocol accepted")
	}
}

func TestEmptyEnvironmentAllowlistDoesNotInheritHost(t *testing.T) {
	m := &EnvironmentManifest{}
	env := m.FilterEnvironment([]string{"SECRET=private-value"})
	if env == nil || len(env) != 0 {
		t.Fatal("empty allowlist must produce a non-nil empty process environment")
	}
}

func TestEnvironmentRunnerPrereleaseAndBuildPin(t *testing.T) {
	m := pinnedEnvironment()
	m.Runners["runner"] = "1.2.3-beta+build.1"
	m.Digest = m.ComputeDigest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	report := &ProbeResult{Checks: []ProbeCheck{{Name: ProbeVersion, Passed: true, Runner: "runner", Version: "1.2.3-beta+build.1"}}}
	if err := m.CheckRunners(report.RunnerVersions()); err != nil {
		t.Fatal(err)
	}
	report.Checks[0].Version = "1.2.3-beta+build.2"
	if err := m.CheckRunners(report.RunnerVersions()); err == nil {
		t.Fatal("changed build accepted")
	}
}
