package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestLocalAnyAgentIsolationNeverProbesNatively(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "native-probe")
	writeSelectionProbe(t, dir, marker)
	s := NewScriptExecutor(dir, t.TempDir())
	for _, isolation := range []string{
		`{"mode":"required","runtime":"unavailable"}`,
		`{"mode":"required","runtime":"bwrap"}`,
		`{"mode":"required","runtime":"oci","image":"approved-image"}`,
		`{"mode":"required","runtime":"docker-vm","image":"approved-image","enabled":true}`,
	} {
		t.Setenv("PATH", t.TempDir())
		_, err := s.SelectExecutor(ExecuteRequest{Executor: "any", WorkspaceMode: "empty", Params: json.RawMessage(`{"isolation":` + isolation + `}`)}, nil)
		if err == nil {
			t.Fatalf("unavailable isolation selected: %s", isolation)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("native probe ran despite required isolation: %v", err)
	}
}

func TestLocalAnyAgentSelectionProbesWithAvailableIsolation(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires Linux or macOS")
	}
	dir, bin := t.TempDir(), t.TempDir()
	marker := filepath.Join(dir, "native-probe")
	record := filepath.Join(bin, "launches")
	writeSelectionProbe(t, dir, marker)
	// This CLI fixture verifies launch routing and cleanup. Actual isolation
	// enforcement is covered by the sandbox conformance suite.
	cli := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + record + "'\ncase \"$*\" in *--muxlio-probe*) printf '%s' '{\"ready\":true,\"checks\":[{\"name\":\"authentication\",\"passed\":true}]}' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(cli), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	backend := "oci"
	if runtime.GOOS == "darwin" {
		backend = "docker-vm"
	}
	params, _ := json.Marshal(map[string]any{"isolation": map[string]any{"mode": "required", "runtime": backend, "image": "approved-image", "enabled": true}})
	s := NewScriptExecutor(dir, t.TempDir())
	name, err := s.SelectExecutor(ExecuteRequest{Executor: "any", WorkspaceMode: "empty", Params: params}, nil)
	if err != nil || name != "agent/test" {
		t.Fatalf("isolated selection = %q, %v", name, err)
	}
	workspace := t.TempDir()
	s.Workspace = workspace
	s.WorkerID = "local"
	s.Workspaces = WorkspaceRegistry{"project": {WorkerID: "local", Path: workspace}}
	name, err = s.SelectExecutor(ExecuteRequest{Executor: "any", WorkspaceID: "project", WorkspaceWorkerID: "local", WorkspaceMode: "shared-write", ConcurrencyKeys: []string{"workspace-lock"}, Params: params}, nil)
	if err != nil || name != "agent/test" {
		t.Fatalf("shared workspace selection = %q, %v", name, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("adapter probed natively: %v", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), workspace+":") {
		t.Fatal("selection mounted workspace before resource admission")
	}
	for _, arg := range []string{"--network=none", "--read-only", "--muxlio-probe", "approved-image", "rm\n--force"} {
		if !strings.Contains(string(data), arg) {
			t.Fatalf("missing sandbox argument %q: %s", arg, data)
		}
	}
	entries, err := os.ReadDir(s.workBase)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe scratch not cleaned: %v, %v", entries, err)
	}
}

func TestLocalAnyAgentSelectionHonorsOperatorIsolation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "native-probe")
	writeSelectionProbe(t, dir, marker)
	s := NewScriptExecutor(dir, t.TempDir())
	s.Executors = map[string]ExecutorConfig{"agent/test": {Authorization: &contract.AuthorizationPolicy{Mode: "restricted", Isolation: "required"}}}
	if _, err := s.SelectExecutor(ExecuteRequest{Executor: "any", WorkspaceMode: "empty"}, nil); err == nil {
		t.Fatal("operator isolation requirement ignored")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("operator-required isolation bypassed: %v", err)
	}
}

func writeSelectionProbe(t *testing.T, dir, marker string) {
	t.Helper()
	script := "#!/bin/sh\necho native > '" + marker + "'\nprintf '%s' '{\"ready\":true,\"checks\":[{\"name\":\"authentication\",\"passed\":true}]}'\n"
	if err := os.WriteFile(filepath.Join(dir, "agent-test"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-test.json"), []byte(`{"manifestVersion":"1","contractVersion":"1","probeVersion":"1","executor":"agent/test"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAnyAgentSelectionUsesReadinessAndOperatorPermissions(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"agent/a", "agent/b"} {
		filename := "agent-" + name[len("agent/"):]
		manifest := `{"manifestVersion":"1","contractVersion":"1","probeVersion":"1","executor":"` + name + `"}`
		if err := os.WriteFile(filepath.Join(dir, filename+".json"), []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
		ready := "false"
		if name == "agent/b" {
			ready = "true"
		}
		script := "#!/bin/sh\nprintf '%s' '{\"ready\":" + ready + ",\"checks\":[{\"name\":\"authentication\",\"passed\":" + ready + "}]}'\n"
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := NewScriptExecutor(dir, t.TempDir())
	req := ExecuteRequest{Executor: "any", WorkspaceMode: "empty"}
	name, err := s.SelectExecutor(req, map[string]int{"agent/b": 8})
	if err != nil || name != "agent/b" {
		t.Fatalf("selection = %q, %v", name, err)
	}
	denied := false
	s.Executors = map[string]ExecutorConfig{"agent/b": {Allow: &denied}}
	if _, err := s.SelectExecutor(req, nil); err == nil {
		t.Fatal("operator denial ignored")
	}
}

func TestAnyAgentPolicyRequiresConcretePermission(t *testing.T) {
	for _, allowed := range [][]string{nil, {"any"}, {"agent/any"}, {"command"}} {
		if _, err := GuardSelectionRequest(ExecuteRequest{Executor: "any", Policy: &ExecutionPolicy{Required: true, AllowedCapabilities: allowed}}); err == nil {
			t.Fatalf("accepted nonconcrete permissions: %v", allowed)
		}
	}
}

func TestSelectAgentPrefersVarietyAndReusesOnlyCandidate(t *testing.T) {
	for _, tc := range []struct {
		candidates []string
		usage      map[string]int
		want       string
	}{
		{[]string{"agent/b", "agent/a", "agent/b"}, nil, "agent/a"},
		{[]string{"agent/a", "agent/b"}, map[string]int{"agent/a": 1}, "agent/b"},
		{[]string{"agent/a"}, map[string]int{"agent/a": 9}, "agent/a"},
	} {
		got, err := SelectAgent(tc.candidates, tc.usage)
		if err != nil || got != tc.want {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
	if _, err := SelectAgent(nil, nil); err == nil {
		t.Fatal("empty candidates accepted")
	}
}

func TestChooseAgentRanksAvailabilityDiversityLoadAndRotation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		candidates  []AgentCandidate
		preferences *AgentSelectionPreferences
		want        string
	}{
		{"available", []AgentCandidate{{Executor: "agent/a", Capacity: 1}, {Executor: "agent/b", Available: true, Capacity: 1}}, &AgentSelectionPreferences{PreviousExecutors: []string{"agent/b"}}, "agent/b"},
		{"dependency diversity", []AgentCandidate{{Executor: "agent/a", Available: true, Capacity: 10}, {Executor: "agent/b", Available: true, Capacity: 10, Load: 1}}, &AgentSelectionPreferences{PreviousExecutors: []string{"agent/a"}}, "agent/b"},
		{"current load", []AgentCandidate{{Executor: "agent/a", Available: true, Capacity: 10, Load: 2}, {Executor: "agent/b", Available: true, Capacity: 10, Load: 1}}, nil, "agent/b"},
		{"rotation", []AgentCandidate{{Executor: "agent/a", Available: true, Capacity: 1, LastTurn: 2}, {Executor: "agent/b", Available: true, Capacity: 1, LastTurn: 1}}, nil, "agent/b"},
		{"fresh retry waits", []AgentCandidate{{Executor: "agent/a", Available: true, Capacity: 1}, {Executor: "agent/b", Capacity: 1}}, &AgentSelectionPreferences{FailedExecutors: []string{"agent/a"}}, "agent/b"},
		{"single failed agent", []AgentCandidate{{Executor: "agent/a", Available: true, Capacity: 1}}, &AgentSelectionPreferences{FailedExecutors: []string{"agent/a"}}, "agent/a"},
		{"all failed avoids last", []AgentCandidate{{Executor: "agent/a", Available: true, Capacity: 1}, {Executor: "agent/b", Capacity: 1}}, &AgentSelectionPreferences{FailedExecutors: []string{"agent/a", "agent/b"}, LastFailedExecutor: "agent/a"}, "agent/b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChooseAgent(tc.candidates, tc.preferences)
			if err != nil || got.Executor != tc.want {
				t.Fatalf("got %+v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestCompatibleAgentsEnforcesConcreteContractAndPolicy(t *testing.T) {
	w := state.Worker{ID: "worker", Capabilities: []state.WorkerCapability{
		{Name: "command"},
		{Name: "agent/any"},
		{Name: "agent/a", Readiness: &contract.ProbeResult{Ready: false}},
		{Name: "agent/b"},
		{Name: "agent/c"},
	}}
	req := ExecuteRequest{Executor: "any", Policy: &ExecutionPolicy{Required: true, AllowedCapabilities: []string{"agent/b"}}}
	names := CompatibleAgents(req, w)
	if len(names) != 1 || names[0] != "agent/b" {
		t.Fatalf("candidates = %v", names)
	}
	req.RequiredCapabilities = map[string]string{contract.CapabilitySession: contract.SessionVersion}
	if names := CompatibleAgents(req, w); len(names) != 0 {
		t.Fatalf("unsupported session accepted: %v", names)
	}
	req.RequiredCapabilities = nil
	req.PlacementLabels = map[string]string{"zone": "required"}
	if names := CompatibleAgents(req, w); len(names) != 0 {
		t.Fatalf("placement ignored: %v", names)
	}
	req.PlacementLabels = nil
	req.ExecutorVersion = "pinned"
	if names := CompatibleAgents(req, w); len(names) != 0 {
		t.Fatalf("version ignored: %v", names)
	}
}

func TestAnyAgentResumeKeepsSessionOwner(t *testing.T) {
	descriptor := &contract.AdapterManifest{Capabilities: map[string]bool{contract.CapabilitySession: true}, CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}}
	w := state.Worker{ID: "owner", Labels: map[string]string{SessionEnvironmentLabel: "env"}, Capabilities: []state.WorkerCapability{{Name: "agent/a", Contract: descriptor}, {Name: "agent/b", Contract: descriptor}}}
	req := ExecuteRequest{Executor: "any", Session: &contract.SessionRequest{Mode: "resume", Handle: &contract.SessionHandle{ID: "session", Executor: "agent/b", RunnerVersion: "1", OwnerWorker: "owner", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour)}}}
	names := CompatibleAgents(req, w)
	if len(names) != 1 || names[0] != "agent/b" {
		t.Fatalf("session candidates = %v", names)
	}
	w.ID = "other"
	if names := CompatibleAgents(req, w); len(names) != 0 {
		t.Fatalf("session moved workers: %v", names)
	}
}
