package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

// idempotentStubRunner is a runner stub that also implements idempotentRunner.
type idempotentStubRunner struct {
	runs  map[string]idempotentRunEntry // key → entry
	calls int
}

type idempotentRunEntry struct {
	runID       string
	planDigest  string
	inputDigest string
	workspaceID string
}

func newIdempotentStubRunner() *idempotentStubRunner {
	return &idempotentStubRunner{runs: make(map[string]idempotentRunEntry)}
}

func (r *idempotentStubRunner) CreateRun(_ json.RawMessage) (string, error) {
	return "", errors.New("use CreateIdempotentRun")
}

func (r *idempotentStubRunner) Run(_ string) error { return nil }

func (r *idempotentStubRunner) CreateIdempotentRun(p *plan.Plan, input json.RawMessage, workspaceID, key string) (string, bool, error) {
	r.calls++
	if entry, ok := r.runs[key]; ok {
		if entry.planDigest == p.Digest && entry.workspaceID == workspaceID {
			return entry.runID, true, nil
		}
		return "", false, state.ErrIdempotencyKeyConflict
	}
	runID := "run_idempotent_" + key
	r.runs[key] = idempotentRunEntry{runID: runID, planDigest: p.Digest, workspaceID: workspaceID}
	return runID, false, nil
}

const minimalPlanJSON = `{
  "irVersion": "1.0",
  "name": "minimal",
  "subgraphs": {"root": {"nodes": []}}
}`

func postRun(handler http.Handler, body, ikey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ikey != "" {
		req.Header.Set("Idempotency-Key", ikey)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// TestIdempotentRun_FirstSubmission verifies that a new idempotency key
// creates a run and returns 201 Created.
func TestIdempotentRun_FirstSubmission(t *testing.T) {
	runner := newIdempotentStubRunner()
	handler := NewRouter(state.New(), runner)
	body := `{"plan":` + minimalPlanJSON + `,"input":{}}`

	w := postRun(handler, body, "key-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("first submission: status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["runId"] == "" {
		t.Fatal("runId missing from response")
	}
	if runner.calls != 1 {
		t.Fatalf("expected 1 call to CreateIdempotentRun, got %d", runner.calls)
	}
}

// TestIdempotentRun_Duplicate verifies that reusing the same key with the
// same payload returns 200 OK (not 201) and the same run ID.
func TestIdempotentRun_Duplicate(t *testing.T) {
	runner := newIdempotentStubRunner()
	handler := NewRouter(state.New(), runner)
	body := `{"plan":` + minimalPlanJSON + `,"input":{}}`

	w1 := postRun(handler, body, "key-dup")
	if w1.Code != http.StatusCreated {
		t.Fatalf("first: status = %d, body = %s", w1.Code, w1.Body.String())
	}
	var r1 map[string]string
	_ = json.NewDecoder(w1.Body).Decode(&r1)

	w2 := postRun(handler, body, "key-dup")
	if w2.Code != http.StatusOK {
		t.Fatalf("duplicate: expected 200, got %d, body = %s", w2.Code, w2.Body.String())
	}
	var r2 map[string]string
	_ = json.NewDecoder(w2.Body).Decode(&r2)
	if r1["runId"] != r2["runId"] {
		t.Fatalf("duplicate returned different runId: first=%q retry=%q", r1["runId"], r2["runId"])
	}
}

// TestIdempotentRun_Conflict verifies that reusing a key with a different
// payload returns 409 Conflict with MUXLIO_IDEMPOTENCY_KEY_CONFLICT.
func TestIdempotentRun_Conflict(t *testing.T) {
	runner := newIdempotentStubRunner()
	handler := NewRouter(state.New(), runner)

	body1 := `{"plan":` + minimalPlanJSON + `,"input":{}}`
	w1 := postRun(handler, body1, "key-conflict")
	if w1.Code != http.StatusCreated {
		t.Fatalf("first: status = %d, body = %s", w1.Code, w1.Body.String())
	}

	// Second submission changes the plan name — the stub uses planDigest which
	// will differ once ComputeDigest is called on the modified plan.
	// Here we simulate conflict by making the stub detect it via our entry.
	// We inject a different planDigest by using a plan whose digest the stub
	// won't match (the stub compares plan.Digest which will be empty string
	// since the test body has no digest field).  Reuse same key → conflict.
	body2 := `{"plan":{"irVersion":"1.0","name":"other","subgraphs":{"root":{"nodes":[]}}},"input":{}}`
	w2 := postRun(handler, body2, "key-conflict")
	if w2.Code != http.StatusConflict {
		t.Fatalf("conflict: expected 409, got %d, body = %s", w2.Code, w2.Body.String())
	}
	env := decodeError(t, w2)
	if env.Code != CodeIdempotencyKeyConflict {
		t.Fatalf("conflict code = %q, want %q", env.Code, CodeIdempotencyKeyConflict)
	}
	if env.Fix == "" {
		t.Error("conflict error has no fix message")
	}
}

// TestIdempotentRun_NoKeyFallsThrough verifies that when no Idempotency-Key
// header is set the request uses the normal run-creation path.
func TestIdempotentRun_NoKeyFallsThrough(t *testing.T) {
	runner := newIdempotentStubRunner()
	handler := NewRouter(state.New(), runner)
	body := `{"plan":` + minimalPlanJSON + `,"input":{}}`

	w := postRun(handler, body, "") // no key
	// Without a key the handler falls through to CreateRun which the stub
	// returns an error for; the important thing is that CreateIdempotentRun
	// was NOT called.
	if runner.calls != 0 {
		t.Fatalf("expected 0 idempotent calls without a key, got %d", runner.calls)
	}
	_ = w // status may be 500 from stubRunner.CreateRun; that's fine
}

// TestIdempotentRun_KeyConflictEnvelopeShape verifies that the conflict error
// envelope has the full structured fields expected by coding agents.
func TestIdempotentRun_KeyConflictEnvelopeShape(t *testing.T) {
	runner := newIdempotentStubRunner()
	handler := NewRouter(state.New(), runner)

	body := `{"plan":` + minimalPlanJSON + `,"input":{}}`
	postRun(handler, body, "key-shape")

	body2 := `{"plan":{"irVersion":"1.0","name":"different","subgraphs":{"root":{"nodes":[]}}},"input":{}}`
	w := postRun(handler, body2, "key-shape")
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
	env := decodeError(t, w)
	assertErrorEnvelope(t, env, CodeIdempotencyKeyConflict)
}

func TestIdempotentRunCallerIsolation(t *testing.T) {
	runner := newIdempotentStubRunner()
	handler := NewRouter(state.New(), runner)
	body := `{"plan":` + minimalPlanJSON + `,"input":{}}`
	submit := func(caller string) string {
		req := httptest.NewRequest("POST", "/v1/runs", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "shared")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, withCaller(req, Caller{ID: caller, IsAdmin: true}))
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp["runId"]
	}
	a, b := submit("caller-a"), submit("caller-b")
	if a == b || submit("caller-a") != a || submit("caller-b") != b {
		t.Fatal("caller keys not isolated or stable")
	}
}

func TestIdempotentRunRejectsInvalidDeclaredDigest(t *testing.T) {
	runner := newIdempotentStubRunner()
	body := `{"plan":{"irVersion":"1.0","name":"minimal","digest":"sha256:wrong","subgraphs":{"root":{"nodes":[]}}},"input":{}}`
	w := postRun(NewRouter(state.New(), runner), body, "key")
	if w.Code != 400 || runner.calls != 0 {
		t.Fatalf("invalid digest: %d %s calls=%d", w.Code, w.Body.String(), runner.calls)
	}
}

func TestIdempotentRunUnsupportedRunnerFailsClosed(t *testing.T) {
	w := postRun(NewRouter(state.New(), &stubRunner{}), `{"plan":`+minimalPlanJSON+`,"input":{}}`, "key")
	if w.Code != 503 {
		t.Fatalf("unsupported runner: %d %s", w.Code, w.Body.String())
	}
}
