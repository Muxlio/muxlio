package main

import (
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfiguredIsolationBackendsCanReceiveAssignments(t *testing.T) {
	dir := t.TempDir()
	binary, backend := "bwrap", "bwrap"
	if runtime.GOOS == "darwin" {
		binary, backend = "docker", "docker-vm"
	} else if runtime.GOOS != "linux" {
		t.Skip("no supported sandbox backend")
	}
	if err := os.WriteFile(filepath.Join(dir, binary), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"isolationBackends":["`+backend+`"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := localWorkerSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	labels, err := workers.IsolationLabels(cfg.IsolationBackends)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"executor":"test","params":{"isolation":{"mode":"required","runtime":"` + backend + `","image":"approved-image","enabled":true}}}`)
	caps := []state.WorkerCapability{{Name: "test"}}
	if !workers.MatchDispatch("test", raw, caps, labels) {
		t.Fatal("configured isolation backend never matches")
	}
	if workers.MatchDispatch("test", raw, caps, nil) {
		t.Fatal("unconfigured isolation advertised")
	}
	if _, err := workers.IsolationLabels([]string{"unknown"}); err == nil {
		t.Fatal("unknown backend accepted")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := workers.IsolationLabels(cfg.IsolationBackends); err == nil {
		t.Fatal("missing launcher advertised")
	}
}
