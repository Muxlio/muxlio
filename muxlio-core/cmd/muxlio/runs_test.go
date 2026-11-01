package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

// seedStore creates an in-memory store seeded with one run per state string.
// It returns the store and a slice of the created run IDs in the same order.
func seedStore(t *testing.T, states []string) state.Store {
	t.Helper()
	store := state.New()
	for i, s := range states {
		r := &state.Run{
			ID:           strings.ReplaceAll("run-"+s, " ", "-") + "-" + string(rune('a'+i)),
			WorkflowName: "wf-" + s,
		}
		if err := store.CreateRun(r); err != nil {
			t.Fatalf("CreateRun[%d]: %v", i, err)
		}
		// CreateRun always sets state to "created"; transition to the desired state.
		if s != "created" {
			if err := store.SetRunState(r.ID, s); err != nil {
				t.Fatalf("SetRunState %q → %q: %v", r.ID, s, err)
			}
		}
	}
	return store
}

// runCmdRuns captures stdout, calls cmdRunsWithStore, restores stdout, and
// returns (captured output, exit code).
func runCmdRuns(t *testing.T, store state.Store, filters []string, jsonOut bool) (string, int) {
	t.Helper()
	old, r, w := captureStdout(t)
	defer restoreStdout(old)

	code := cmdRunsWithStore(store, filters, jsonOut)

	w.Close()
	var buf strings.Builder
	b := make([]byte, 8192)
	for {
		n, err := r.Read(b)
		if n > 0 {
			buf.Write(b[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String(), code
}

// ---------- plain-text output ----------

func TestCmdRuns_NoFilter_ReturnsAll(t *testing.T) {
	store := seedStore(t, []string{"succeeded", "failed", "running"})
	out, code := runCmdRuns(t, store, nil, false)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	for _, s := range []string{"succeeded", "failed", "running"} {
		if !strings.Contains(out, s) {
			t.Errorf("state %q missing from unfiltered output:\n%s", s, out)
		}
	}
}

func TestCmdRuns_SingleStateFilter(t *testing.T) {
	store := seedStore(t, []string{"succeeded", "failed", "running"})
	out, code := runCmdRuns(t, store, []string{"succeeded"}, false)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	if !strings.Contains(out, "succeeded") {
		t.Errorf("expected 'succeeded' in output:\n%s", out)
	}
	if strings.Contains(out, "failed") {
		t.Errorf("unexpected 'failed' in output:\n%s", out)
	}
	if strings.Contains(out, "running") {
		t.Errorf("unexpected 'running' in output:\n%s", out)
	}
}

func TestCmdRuns_MultipleStateFilters(t *testing.T) {
	store := seedStore(t, []string{"succeeded", "failed", "running", "paused"})
	out, code := runCmdRuns(t, store, []string{"succeeded", "failed"}, false)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	if !strings.Contains(out, "succeeded") {
		t.Errorf("expected 'succeeded' in output:\n%s", out)
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("expected 'failed' in output:\n%s", out)
	}
	if strings.Contains(out, "running") {
		t.Errorf("unexpected 'running' in output:\n%s", out)
	}
	if strings.Contains(out, "paused") {
		t.Errorf("unexpected 'paused' in output:\n%s", out)
	}
}

func TestCmdRuns_FilterMatchesNone_PrintsNoRuns(t *testing.T) {
	store := seedStore(t, []string{"succeeded", "running"})
	out, code := runCmdRuns(t, store, []string{"failed"}, false)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	if !strings.Contains(out, "no runs") {
		t.Errorf("expected 'no runs' message, got:\n%s", out)
	}
}

func TestCmdRuns_EmptyStore_PrintsNoRuns(t *testing.T) {
	store := state.New()
	out, code := runCmdRuns(t, store, nil, false)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	if !strings.Contains(out, "no runs") {
		t.Errorf("expected 'no runs' message, got:\n%s", out)
	}
}

// ---------- JSON output ----------

func TestCmdRuns_JSONOutput_NoFilter_AllRunsPresent(t *testing.T) {
	store := seedStore(t, []string{"succeeded", "failed"})
	out, code := runCmdRuns(t, store, nil, true)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	var result struct {
		Runs []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}
	if len(result.Runs) != 2 {
		t.Errorf("expected 2 runs in JSON, got %d:\n%s", len(result.Runs), out)
	}
	// JSON envelope must use the "runs" key for backward compatibility.
	if !strings.Contains(out, `"runs"`) {
		t.Errorf(`JSON output missing "runs" key: %s`, out)
	}
}

func TestCmdRuns_JSONOutput_WithStateFilter(t *testing.T) {
	store := seedStore(t, []string{"succeeded", "failed", "running"})
	out, code := runCmdRuns(t, store, []string{"succeeded"}, true)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	var result struct {
		Runs []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}
	if len(result.Runs) != 1 {
		t.Errorf("expected 1 run in JSON after state filter, got %d:\n%s", len(result.Runs), out)
	}
	if result.Runs[0].State != "succeeded" {
		t.Errorf("expected run state 'succeeded', got %q", result.Runs[0].State)
	}
}

func TestCmdRuns_JSONOutput_FilterMatchesNone_EmptyArray(t *testing.T) {
	store := seedStore(t, []string{"running"})
	out, code := runCmdRuns(t, store, []string{"succeeded"}, true)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; output: %s", code, out)
	}
	// Must be valid JSON with a "runs" key.
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}
	if _, ok := result["runs"]; !ok {
		t.Errorf(`JSON output missing "runs" key: %s`, out)
	}
}

// ---------- error handling ----------

func TestCmdRuns_UnknownState_ExitCode2(t *testing.T) {
	store := state.New()
	// Redirect stderr to discard so the test log stays clean.
	origStderr := os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stderr = devNull
	code := cmdRunsWithStore(store, []string{"bogus"}, false)
	os.Stderr = origStderr
	devNull.Close()
	if code != 2 {
		t.Errorf("expected exit 2 for unknown state, got %d", code)
	}
}

func TestCmdRuns_UnknownStateAmongValid_ExitCode2(t *testing.T) {
	store := seedStore(t, []string{"succeeded"})
	origStderr := os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stderr = devNull
	code := cmdRunsWithStore(store, []string{"succeeded", "INVALID"}, false)
	os.Stderr = origStderr
	devNull.Close()
	if code != 2 {
		t.Errorf("expected exit 2 when one unknown state is present, got %d", code)
	}
}

// ---------- parseStateFlag ----------

func TestParseStateFlag_CommaSeparated(t *testing.T) {
	result := parseStateFlag("succeeded,failed, running ")
	want := []string{"succeeded", "failed", "running"}
	if len(result) != len(want) {
		t.Fatalf("parseStateFlag got %v, want %v", result, want)
	}
	for i, s := range want {
		if result[i] != s {
			t.Errorf("parseStateFlag[%d] = %q, want %q", i, result[i], s)
		}
	}
}

func TestParseStateFlag_SingleValue(t *testing.T) {
	result := parseStateFlag("failed")
	if len(result) != 1 || result[0] != "failed" {
		t.Errorf("parseStateFlag(\"failed\") = %v, want [failed]", result)
	}
}

func TestParseStateFlag_EmptyTokensDropped(t *testing.T) {
	result := parseStateFlag(",, ,")
	if len(result) != 0 {
		t.Errorf("parseStateFlag with only separators should return empty, got %v", result)
	}
}
