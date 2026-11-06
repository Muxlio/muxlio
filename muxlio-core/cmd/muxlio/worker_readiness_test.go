package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerStatusExplainsReadiness(t *testing.T) {
	dir := t.TempDir()
	adapters := filepath.Join(dir, "libexec", "muxlio", "adapters")
	if err := os.MkdirAll(adapters, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adapters, "test.json"), []byte(`{"contractVersion":"1","probeVersion":"1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s' '{"ready":false,"checks":[{"name":"authentication","passed":false,"message":"run runner login"}]}'
`
	if err := os.WriteFile(filepath.Join(adapters, "test"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	caps, err := workerReadiness(dir, "")
	if err != nil || len(caps) != 1 {
		t.Fatalf("reports: %v %v", caps, err)
	}
	if caps[0].Readiness.Ready || caps[0].Readiness.Failed()[0].Message != "run runner login" {
		t.Fatal(caps)
	}
	if code := cmdWorkerStatus(dir, "", true); code != 1 {
		t.Fatalf("status exit = %d", code)
	}

	// Doctor must probe the configured adapter directory, just like status.
	configured := filepath.Join(dir, "configured-adapters")
	if err := os.Rename(adapters, configured); err != nil {
		t.Fatal(err)
	}
	rawConfig, err := json.Marshal(map[string]string{"adapters": configured})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authConfigPath(dir), rawConfig, 0600); err != nil {
		t.Fatal(err)
	}

	output, err := os.CreateTemp(t.TempDir(), "doctor-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	original := os.Stdout
	os.Stdout = output
	code := cmdDoctor([]string{"--server", "http://127.0.0.1:1", "--json"}, dir, true)
	os.Stdout = original
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(raw), "test / authentication") || !strings.Contains(string(raw), "run runner login") {
		t.Fatalf("doctor did not explain readiness: %s", raw)
	}
}

// Existing service fixtures explicitly implement the shared probe protocol.
func readyFixtureAdapter(t *testing.T, path, script string) string {
	t.Helper()
	if err := os.WriteFile(path+".json", []byte(`{"contractVersion":"1","probeVersion":"1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return "#!/bin/sh\nif [ \"$1\" = --muxlio-probe ]; then printf '%s' '{\"ready\":true,\"checks\":[{\"name\":\"version\",\"passed\":true}]}'; exit 0; fi\n" + script
}
