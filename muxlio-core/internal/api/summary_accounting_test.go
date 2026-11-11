package api

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"net/http/httptest"
	"testing"
)

func TestRunSummaryExposesOneCanonicalAccountingShape(t *testing.T) {
	s := state.New()
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "a1", RunID: "run", State: "succeeded", Accounting: testaccounting.Tokens(100, 20, 30)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "a2", RunID: "run", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/runs/run", "/v1/runs/run?summary=true"} {
		w := httptest.NewRecorder()
		NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s", w.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if path[len(path)-4:] == "true" {
			if err := json.Unmarshal(raw["overview"], &raw); err != nil {
				t.Fatal(err)
			}
		}
		for _, old := range []string{"usage", "measurements", "tokens", "costUsd", "credits"} {
			if _, exists := raw[old]; exists {
				t.Fatalf("legacy alias %s", old)
			}
		}
		var summary state.AccountingSummary
		if err := json.Unmarshal(raw["accounting"], &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Tokens.KnownSubtotal == nil || *summary.Tokens.KnownSubtotal != "120" || summary.Tokens.EffectiveTotal != nil || summary.Tokens.Attempts.Unknown != 1 {
			t.Fatalf("summary=%+v", summary)
		}
	}
}
