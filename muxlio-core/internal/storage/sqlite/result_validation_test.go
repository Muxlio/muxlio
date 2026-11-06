package sqlite

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestResultValidationSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	a := state.ExecutionAttempt{ID: "attempt", RunID: "r", ActivationID: "r:task", State: "failed", StartedAt: time.Now(), FinishedAt: time.Now(), Result: &state.AttemptResult{
		ExecutionSucceeded: true,
		Outputs:            map[string]any{"approval": "looks good", "notes": map[string]any{"digest": "sha256:test", "mime": "text/markdown", "path": "notes.md"}},
		ValidationFailure:  &state.ValueValidationFailure{Code: "MUXLIO_OUTPUT_VALIDATION_FAILED", Path: "approval", Expected: json.RawMessage(`{"type":"boolean"}`), Actual: json.RawMessage(`"looks good"`), Message: "expected boolean"},
	}}
	a.Failure = a.Result.ValidationFailure.Error()
	if err := st.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	attempts, err := st.ListExecutionAttempts("r", "")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	got := attempts[0]
	if got.State != "failed" || got.Result == nil || !got.Result.ExecutionSucceeded || got.Result.ValidationFailure.Code != "MUXLIO_OUTPUT_VALIDATION_FAILED" || string(got.Result.ValidationFailure.Actual) != `"looks good"` {
		t.Fatalf("lost validation result: %+v", got)
	}
	if got.Result.Outputs["notes"].(map[string]any)["path"] != "notes.md" {
		t.Fatalf("lost artifact: %+v", got.Result)
	}
	events, err := st.GetEvents("r", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := events[len(events)-1].Payload["result"].(map[string]any)
	if !ok || payload["executionSucceeded"] != true {
		t.Fatalf("event lost execution outcome: %v", events)
	}
	// A legacy database has no result column. Opening it must migrate in place.
	if _, err := st.db.Exec(`ALTER TABLE execution_attempts DROP COLUMN result_json`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	attempts, err = st.ListExecutionAttempts("r", "")
	if err != nil || len(attempts) != 1 || attempts[0].Result != nil {
		t.Fatalf("legacy migration: %v %v", attempts, err)
	}
}
