package workers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/sandbox"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestProbeReadinessAndMatching(t *testing.T) {
	for _, category := range []string{"authentication", "backend", "tools", "version"} {
		t.Run(category, func(t *testing.T) {
			dir := t.TempDir()
			manifest := `{"manifestVersion":"1","contractVersion":"1","executor":"test","probeVersion":"1"}`
			if err := os.WriteFile(filepath.Join(dir, "test.json"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
[ "$1" = "--muxlio-probe" ] || exit 9
printf '%s' '{"ready":true,"checks":[{"name":"` + category + `","passed":false,"message":"requirement unavailable"}]}'
`
			if err := os.WriteFile(filepath.Join(dir, "test"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			e := NewScriptExecutor(dir, dir)
			report := e.Probe(context.Background(), "test")
			if report.Ready || len(report.Failed()) != 1 || report.Failed()[0].Name != category {
				t.Fatalf("unexpected report: %+v", report)
			}
			caps := []state.WorkerCapability{{Name: "test", Readiness: &report}}
			if MatchDispatch("test", nil, caps, nil) {
				t.Fatal("unready executor matched before lease")
			}
		})
	}
}

func TestProbeDoesNotInvokeLegacyOrDeniedExecutor(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	if err := os.WriteFile(filepath.Join(dir, "test"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e := NewScriptExecutor(dir, dir)
	if e.Probe(context.Background(), "test").Ready {
		t.Fatal("legacy adapter is unverified")
	}
	denied := false
	e.Executors = map[string]ExecutorConfig{"test": {Allow: &denied}}
	r := e.Probe(context.Background(), "test")
	if r.Ready || r.Failed()[0].Name != contract.ProbePermission {
		t.Fatal(r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsafe adapter invocation")
	}
}

func TestProbeUsesConfiguredEnvironmentAndRejectsInvalidOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.json"), []byte(`{"contractVersion":"1","probeVersion":"1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	e := NewScriptExecutor(dir, dir)
	e.WorkerEnv = map[string]string{"PROBE_TEST": "configured"}
	for _, output := range []string{`{"ready":true,"checks":[{"name":"version","passed":true}]}`, `{}`, `{"outputs":{}}`, `not json`} {
		script := "#!/bin/sh\n[ \"$PROBE_TEST\" = configured ] || exit 1\nprintf '%s' '" + output + "'\n"
		if err := os.WriteFile(filepath.Join(dir, "test"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		r := e.Probe(context.Background(), "test")
		if r.Ready != (output == `{"ready":true,"checks":[{"name":"version","passed":true}]}`) {
			t.Fatalf("%s: %+v", output, r)
		}
	}
}

func TestProbeCancellationAndMissingReportPreventMatching(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.json"), []byte(`{"contractVersion":"1","probeVersion":"1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if NewScriptExecutor(dir, dir).Probe(ctx, "test").Ready {
		t.Fatal("cancelled probe became ready")
	}
	caps := []state.WorkerCapability{{Name: "test", Contract: &contract.AdapterManifest{ProbeVersion: "1"}}}
	if MatchDispatch("test", nil, caps, nil) {
		t.Fatal("missing probe evidence matched")
	}
}

func TestTaskProbeUsesLaunchAndCleanup(t *testing.T) {
	s := testScript(t, "exit 91") // The native adapter must never be invoked.
	if err := os.WriteFile(s.scriptPath("test")+".json", []byte(`{"contractVersion":"1","probeVersion":"1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cleaned := false
	report := s.probe(context.Background(), "test", func() (sandbox.Launch, error) {
		cmd := exec.Command("/bin/sh", "-c", `printf '%s' '{"ready":true,"checks":[{"name":"version","passed":true,"runner":"runner","version":"3.2.1"}]}'`)
		return sandbox.Launch{Command: cmd, Cleanup: func() error { cleaned = true; return nil }}, nil
	})
	if !report.Ready || report.RunnerVersions()["runner"] != "3.2.1" || !cleaned {
		t.Fatalf("task probe did not use launch lifecycle: %+v cleaned=%v", report, cleaned)
	}
}
