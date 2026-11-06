package workers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func environmentDescriptor() *adapterManifest {
	descriptor := &adapterManifest{Executor: "test", ContractVersion: "1", ProbeVersion: "1"}
	m := &contract.EnvironmentManifest{Version: "1", ProtocolDigest: descriptor.ProtocolDigest(), Runners: map[string]string{"runner": "1.2.3"}, Skills: map[string]string{"review": "sha256:" + strings.Repeat("a", 64)}, AllowedEnvironment: []string{"PATH", "HOME"}}
	m.Digest = m.ComputeDigest()
	descriptor.Environment = m
	return descriptor
}

func TestDispatchRequiresPinnedEnvironmentEvidence(t *testing.T) {
	descriptor := environmentDescriptor()
	report := &contract.ProbeResult{Ready: true, Checks: []contract.ProbeCheck{{Name: contract.ProbeVersion, Passed: true, Runner: "runner", Version: "1.2.3"}}}
	req := ExecuteRequest{Executor: "test", Environment: descriptor.Environment}
	raw, _ := json.Marshal(req)
	caps := []state.WorkerCapability{{Name: "test", Contract: descriptor, Readiness: report}}
	if !MatchDispatch("test", raw, caps, nil) {
		t.Fatal("compatible worker rejected")
	}
	caps[0].Contract = nil
	if MatchDispatch("test", raw, caps, nil) {
		t.Fatal("worker lacking manifest received dispatch")
	}
	caps[0].Contract = descriptor
	descriptor.Environment.Skills = nil
	descriptor.Environment.Digest = descriptor.Environment.ComputeDigest()
	if MatchDispatch("test", raw, caps, nil) {
		t.Fatal("worker lacking skill received dispatch")
	}
	descriptor = environmentDescriptor()
	caps[0].Contract = descriptor
	report.Checks[0].Version = "2.0.0"
	if MatchDispatch("test", raw, caps, nil) {
		t.Fatal("worker with incompatible runner received dispatch")
	}
}

func TestLocalDispatchReprobesPinnedRunnerBeforeWork(t *testing.T) {
	s := testScript(t, `if [ "$1" = "--muxlio-probe" ]; then printf '%s' '{"ready":true,"checks":[{"name":"version","passed":true,"runner":"runner","version":"2.0.0"}]}'; exit; fi
 touch launched; printf '{}'`)
	descriptor := environmentDescriptor()
	raw, _ := json.Marshal(descriptor)
	if err := os.WriteFile(s.scriptPath("test")+".json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := s.Execute(ExecuteRequest{Executor: "test", Environment: descriptor.Environment})
	if err != nil || !strings.Contains(result.Error, "pinned runner") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	entries, err := os.ReadDir(s.workBase)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one private probe scratch directory: %v %v", entries, err)
	}
}

func TestPinnedEnvironmentFiltersProbeAndExecution(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "explicit"}[allow], func(t *testing.T) {
			s := testScript(t, `if [ -n "$DROP_VARIABLE" ]; then exit 1; fi
if [ "$KEEP_VARIABLE" != "`+map[bool]string{false: "", true: "public"}[allow]+`" ]; then exit 1; fi
if [ "$1" = "--muxlio-probe" ]; then printf '%s' '{"ready":true,"checks":[{"name":"backend","passed":true}]}'; exit; fi
printf '%s' '{"outputs":{"filtered":true}}'`)
			s.WorkerEnv = map[string]string{"KEEP_VARIABLE": "public", "DROP_VARIABLE": "private-value"}
			descriptor := environmentDescriptor()
			descriptor.Environment.Runners = nil
			descriptor.Environment.AllowedEnvironment = nil
			if allow {
				descriptor.Environment.AllowedEnvironment = []string{"KEEP_VARIABLE"}
			}
			descriptor.Environment.Digest = descriptor.Environment.ComputeDigest()
			raw, err := json.Marshal(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.scriptPath("test")+".json", raw, 0600); err != nil {
				t.Fatal(err)
			}
			result, err := s.Execute(ExecuteRequest{Executor: "test", Environment: descriptor.Environment})
			if err != nil || result.Error != "" || result.Outputs["filtered"] != true {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
