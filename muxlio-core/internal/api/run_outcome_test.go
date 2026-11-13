package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
)

func TestRunOutcome(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, tc := range []struct {
			state, activation, attempt, input, outcome, action string
			terminal                                           bool
		}{
			{"succeeded", "succeeded", "succeeded", "", "succeeded", "inspect_artifacts", true},
			{"failed", "failed", "failed", "", "failed", "inspect_failure", true},
			{"succeeded", "succeeded", "resolved", "", "succeeded", "inspect_artifacts", true},
			{"failed", "failed", "resolved", "", "failed", "inspect_failure", true},
			{"cancelled", "cancelled", "resolved", "", "cancelled", "none", true},
			{"cancelled", "cancelled", "cancelled", "", "cancelled", "none", true},
			{"paused", "waiting", "", "", "paused", "resume", false},
			{"created", "queued", "", "", "queued", "wait", false},
			{"running", "running", "running", "", "running", "wait", false},
			{"waiting", "waiting", "", "pending", "waiting", "provide_input", false},
			{"succeeded", "running", "running", "", "uncertain", "inspect_run", false},
			{"succeeded", "succeeded", "succeeded", "pending", "uncertain", "inspect_run", false},
			{"cancelled", "cancelled", "awaiting_reconciliation", "", "uncertain", "reconcile", false},
			{"unknown", "blocked", "", "", "uncertain", "inspect_run", false},
		} {
			t.Run(backend+"/"+tc.state+"/"+tc.activation+"/"+tc.attempt+"/"+tc.input, func(t *testing.T) {
				var s state.Store = state.New()
				if backend == "sqlite" {
					var err error
					s, err = sqlite.Open(filepath.Join(t.TempDir(), "outcome.db"))
					if err != nil {
						t.Fatal(err)
					}
				}
				defer s.Close()
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				must(s.CreateRun(&state.Run{ID: "run", WorkspaceID: "workspace", Input: "UNTRUSTED_INPUT", Output: "UNTRUSTED_OUTPUT"}))
				must(s.SetRunState("run", "running"))
				must(s.CreateActivation(&state.Activation{ID: "act", RunID: "run", NodeID: "step", State: "running", Error: "failure"}))
				must(s.SetActivationOutputs("act", map[string]any{"report": map[string]any{"digest": "sha256:artifact", "mime": "text/plain", "data": "UNTRUSTED_BYTES"}}))
				must(s.SetActivationState("act", tc.activation))
				if tc.attempt != "" {
					must(s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "act", State: tc.attempt, Accounting: testaccounting.Tokens(0, 0, 0), Result: &state.AttemptResult{Outputs: map[string]any{"data": "UNTRUSTED_RESULT"}}}))
				}
				if tc.input != "" {
					must(s.CreateInputRequest(&state.InputRequest{RunID: "run", ActivationID: "act", RequestID: "input", State: tc.input, Schema: json.RawMessage(`{"description":"UNTRUSTED_SCHEMA"}`)}))
				}
				must(s.AppendLogChunk(state.LogChunk{RunID: "run", ActivationID: "act", Stream: "stdout", Data: []byte("UNTRUSTED_LOG")}))
				must(s.SetRunState("run", tc.state))
				w := httptest.NewRecorder()
				NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/run/summary", nil))
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "UNTRUSTED_") {
					t.Fatal("summary included payloads")
				}
				var got struct {
					Outcome    string  `json:"outcome"`
					Terminal   bool    `json:"terminal"`
					Workspace  string  `json:"workspaceId"`
					StartedAt  *string `json:"startedAt"`
					FinishedAt *string `json:"finishedAt"`
					NextAction struct {
						Classification string `json:"classification"`
					} `json:"nextAction"`
					Accounting state.AccountingSummary `json:"accounting"`
				}
				must(json.Unmarshal(w.Body.Bytes(), &got))
				var details struct {
					Artifacts struct {
						Items []struct {
							Digest string
							Link   string
						}
					}
					Reconciliation struct{ Total int } `json:"reconciliationRequests"`
					InputRequests  struct {
						Items []struct{ RequestID, Link string }
					}
				}
				must(json.Unmarshal(w.Body.Bytes(), &details))
				if len(details.Artifacts.Items) != 1 || details.Artifacts.Items[0].Digest != "sha256:artifact" || details.Artifacts.Items[0].Link != "/v1/artifacts/sha256:artifact" {
					t.Fatalf("artifacts: %s", w.Body.String())
				}
				if tc.attempt == "awaiting_reconciliation" && details.Reconciliation.Total != 1 {
					t.Fatal("held attempt missing")
				}
				if tc.input == "pending" {
					if len(details.InputRequests.Items) != 1 || details.InputRequests.Items[0].RequestID != "input" {
						t.Fatal("pending input missing")
					}
					linked := httptest.NewRecorder()
					NewRouter(s, nil).ServeHTTP(linked, httptest.NewRequest("GET", details.InputRequests.Items[0].Link, nil))
					if linked.Code != 200 {
						t.Fatalf("input inspection link returned %d: %s", linked.Code, linked.Body.String())
					}
				}
				if got.Outcome != tc.outcome || got.Terminal != tc.terminal || got.NextAction.Classification != tc.action || got.Workspace != "workspace" {
					t.Fatalf("summary: %s", w.Body.String())
				}
				if got.StartedAt == nil || (got.FinishedAt != nil) != tc.terminal {
					t.Fatalf("lifecycle: %+v", got)
				}
				if tc.attempt != "" && (got.Accounting.Tokens.KnownSubtotal == nil || *got.Accounting.Tokens.KnownSubtotal != "0") {
					t.Fatal("reported zero lost")
				}
			})
		}
	}
}

func TestRunOutcomeBoundsAndMissingAccounting(t *testing.T) {
	s := state.New()
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateRun(&state.Run{ID: "run"}))
	must(s.SetRunState("run", "running"))
	for i := 0; i < 25; i++ {
		must(s.CreateActivation(&state.Activation{ID: fmt.Sprint(i), RunID: "run", State: "failed"}))
		must(s.RecordExecutionAttempt(state.ExecutionAttempt{ID: fmt.Sprint(i), RunID: "run", State: "failed"}))
	}
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/run/summary", nil))
	var got struct {
		Failed struct {
			Items   []any
			Total   int
			HasMore bool
		} `json:"failedSteps"`
		Accounting state.AccountingSummary
		FinishedAt *string
		Progress   struct{ Fraction *float64 }
	}
	must(json.Unmarshal(w.Body.Bytes(), &got))
	if w.Code != 200 || len(got.Failed.Items) != 20 || got.Failed.Total != 25 || !got.Failed.HasMore || got.Accounting.Tokens.KnownSubtotal != nil || got.FinishedAt != nil {
		t.Fatalf("summary: %s", w.Body.String())
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", "/v1/runs/run/summary", 405}, {"GET", "/v1/runs/missing/summary", 404}} {
		w := httptest.NewRecorder()
		NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}

type changingOutcomeStore struct {
	*state.Memory
	reads int
}

func (s *changingOutcomeStore) LatestEventSeq(id string) (int, error) {
	s.reads++
	seq, err := s.Memory.LatestEventSeq(id)
	if s.reads > 1 {
		seq++
	}
	return seq, err
}

func TestRunOutcomeConcurrentTransitionWithholdsCompletion(t *testing.T) {
	s := &changingOutcomeStore{Memory: state.New()}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run", State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunState("run", "succeeded"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/run/summary", nil))
	var got struct {
		Outcome    string
		Terminal   bool
		FinishedAt *string
		NextAction struct{ Classification string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Terminal || got.Outcome != "uncertain" || got.FinishedAt != nil || got.NextAction.Classification != "refresh" {
		t.Fatalf("summary: %s", w.Body.String())
	}
}

func TestRunOutcomeActiveProgressNeverComplete(t *testing.T) {
	s := state.New()
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunState("run", "running"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "act", RunID: "run", State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewRouter(s, nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/run/summary", nil))
	var got struct {
		Outcome  string
		Terminal bool
		Progress struct{ Fraction float64 }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Terminal || got.Outcome != "running" || got.Progress.Fraction >= 1 || got.Progress.Fraction < 0 {
		t.Fatalf("summary: %s", w.Body.String())
	}
}

func TestRunOutcomeArtifactReferencesFilterBeforePagination(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		for _, count := range []int{1, 25} {
			t.Run(fmt.Sprintf("%s/%d", backend, count), func(t *testing.T) {
				var s state.Store = state.New()
				if backend == "sqlite" {
					var err error
					s, err = sqlite.Open(filepath.Join(t.TempDir(), "outcome.db"))
					if err != nil {
						t.Fatal(err)
					}
				}
				defer s.Close()
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				must(s.CreateRun(&state.Run{ID: "run"}))
				must(s.CreateActivation(&state.Activation{ID: "act", RunID: "run", State: "running"}))
				outputs := map[string]any{}
				// These sort before actual references and matched the former
				// free-text search, filling its entire page with inline outputs.
				for i := 0; i < 25; i++ {
					outputs[fmt.Sprintf("a-sha256:%02d", i)] = map[string]any{"mime": "text/plain", "data": "inline"}
				}
				for i := 0; i < count; i++ {
					outputs[fmt.Sprintf("z-report-%02d", i)] = map[string]any{"digest": fmt.Sprintf("sha256:%064x", i), "mime": "text/plain"}
				}
				must(s.SetActivationOutputs("act", outputs))
				w := httptest.NewRecorder()
				router := NewRouter(s, nil)
				router.ServeHTTP(w, httptest.NewRequest("GET", "/v1/runs/run/summary", nil))
				var got struct {
					Artifacts struct {
						Items   []struct{ Digest, Link string }
						HasMore bool
					}
				}
				must(json.Unmarshal(w.Body.Bytes(), &got))
				want := count
				if want > outcomeDetailLimit {
					want = outcomeDetailLimit
				}
				if w.Code != 200 || len(got.Artifacts.Items) != want || got.Artifacts.HasMore != (count > outcomeDetailLimit) {
					t.Fatalf("summary: %s", w.Body.String())
				}
				for _, item := range got.Artifacts.Items {
					if !strings.HasPrefix(item.Digest, "sha256:") || item.Link != "/v1/artifacts/"+item.Digest {
						t.Fatalf("invalid reference: %+v", item)
					}
				}
				// Ordinary output inspection must continue to include inline ports.
				entries, err := s.(state.RunViewStore).ListOutputPage("run", 0, 100, "")
				must(err)
				if len(entries) != 25+count {
					t.Fatalf("ordinary output list lost ports: %d", len(entries))
				}
			})
		}
	}
}
