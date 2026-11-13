package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceWorkerCapacityConfigAndDoctor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, tc := range []struct {
		body string
		want int
		bad  bool
	}{
		{`{}`, 1, false},
		{`{"workerCapacity":0}`, 0, false},
		{`{"workerCapacity":4}`, 4, false},
		{`{"workerCapacity":-1}`, 0, true},
		{`{"workerCapacity":1.5}`, 0, true},
		{`{"workerCapacity":"4"}`, 0, true},
		{`{"workerCapacity":true}`, 0, true},
		{`{"workerCapacity":null}`, 0, true},
		{`{"WorkerCapacity":null}`, 0, true},
		{`{"workerCapacity":  null }`, 0, true},
		{`{"workerCapacity":9999999999999999999999999}`, 0, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadServiceConfig(path)
			if (err != nil) != tc.bad || (!tc.bad && cfg.WorkerCapacity != tc.want) {
				t.Fatalf("capacity=%d err=%v", cfg.WorkerCapacity, err)
			}
			if tc.bad && !strings.Contains(err.Error(), "workerCapacity must be a nonnegative integer") {
				t.Fatalf("unhelpful validation error: %v", err)
			}
			check := serviceWorkerCapacityCheck(path)
			if check.Passed == tc.bad || !strings.Contains(check.Message, "workerCapacity") {
				t.Fatalf("doctor: %+v", check)
			}
			if !tc.bad && tc.want == 0 && !strings.Contains(check.Message, "draining") {
				t.Fatalf("doctor did not explain zero: %+v", check)
			}
		})
	}
}

func TestServiceWorkerCapacityDefaultWithoutConfig(t *testing.T) {
	t.Setenv("MUXLIO_HOME", t.TempDir())
	t.Setenv("MUXLIO_CONFIG", "")
	cfg, err := loadServiceConfig("")
	if err != nil || cfg.WorkerCapacity != 1 {
		t.Fatalf("default capacity=%d err=%v", cfg.WorkerCapacity, err)
	}
	check := serviceWorkerCapacityCheck(authConfigPath(""))
	if !check.Passed || !strings.Contains(check.Message, "workerCapacity=1") {
		t.Fatalf("doctor without config: %+v", check)
	}
	t.Setenv("MUXLIO_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if check := serviceWorkerCapacityCheck(authConfigPath("")); check.Passed {
		t.Fatalf("doctor accepted missing explicit config: %+v", check)
	}
}
