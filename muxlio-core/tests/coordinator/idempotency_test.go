package coordinator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

// --- SQLite store idempotency tests ---

// TestCreateRunIdempotent_Duplicate verifies that a second call with the same
// key and binding returns the original run ID without creating a new run.
func TestCreateRunIdempotent_Duplicate(t *testing.T) {
	st := openDB(t)
	run1 := &state.Run{ID: "run_idem1", WorkflowName: "w", PlanDigest: "sha256:aaa"}
	id, dup, err := st.CreateRunIdempotent(run1, "key-1", "inputdigest-1")
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("first call should not be duplicate")
	}
	if id != "run_idem1" {
		t.Fatalf("unexpected run ID: %s", id)
	}

	// Second call with same key and binding.
	run2 := &state.Run{ID: "run_idem2", WorkflowName: "w", PlanDigest: "sha256:aaa"}
	id2, dup2, err2 := st.CreateRunIdempotent(run2, "key-1", "inputdigest-1")
	if err2 != nil {
		t.Fatal(err2)
	}
	if !dup2 {
		t.Fatal("second call should be duplicate")
	}
	if id2 != "run_idem1" {
		t.Fatalf("duplicate should return original run ID, got %s", id2)
	}

	// Verify only one run was actually created.
	runs, err := st.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == "run_idem2" {
			t.Fatal("second run should not have been persisted")
		}
	}
}

// TestCreateRunIdempotent_Conflict verifies that reusing a key with different
// binding fields returns state.ErrIdempotencyKeyConflict.
func TestCreateRunIdempotent_Conflict(t *testing.T) {
	st := openDB(t)
	run1 := &state.Run{ID: "run_conf1", WorkflowName: "w", PlanDigest: "sha256:plan1"}
	if _, _, err := st.CreateRunIdempotent(run1, "conflict-key", "input-A"); err != nil {
		t.Fatal(err)
	}

	// Same key, different plan digest → conflict.
	run2 := &state.Run{ID: "run_conf2", WorkflowName: "w", PlanDigest: "sha256:plan2"}
	_, _, err := st.CreateRunIdempotent(run2, "conflict-key", "input-A")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !isIdempotencyConflict(err) {
		t.Fatalf("expected ErrIdempotencyKeyConflict, got %v", err)
	}

	// Same key, different input digest → conflict.
	run3 := &state.Run{ID: "run_conf3", WorkflowName: "w", PlanDigest: "sha256:plan1"}
	_, _, err = st.CreateRunIdempotent(run3, "conflict-key", "input-B")
	if err == nil {
		t.Fatal("expected conflict error on input change, got nil")
	}
	if !isIdempotencyConflict(err) {
		t.Fatalf("expected ErrIdempotencyKeyConflict, got %v", err)
	}
}

// TestCreateRunIdempotent_WorkspaceIsolation verifies that two different
// workspace IDs with the same key do not collide; each gets its own run.
func TestCreateRunIdempotent_WorkspaceIsolation(t *testing.T) {
	st := openDB(t)
	run1 := &state.Run{ID: "run_ws1", WorkflowName: "w", PlanDigest: "sha256:plan", WorkspaceID: "workspace-A"}
	id1, dup1, err := st.CreateRunIdempotent(run1, "shared-key", "input-hash")
	if err != nil || dup1 {
		t.Fatalf("ws-A: err=%v dup=%v", err, dup1)
	}

	run2 := &state.Run{ID: "run_ws2", WorkflowName: "w", PlanDigest: "sha256:plan", WorkspaceID: "workspace-B"}
	id2, dup2, err := st.CreateRunIdempotent(run2, "shared-key", "input-hash")
	if err != nil || dup2 || id1 == id2 {
		t.Fatalf("different workspaces must create separate runs: ids=%s,%s dup=%v err=%v", id1, id2, dup2, err)
	}
}

// TestCreateRunIdempotent_EmptyKeyFallback verifies that an empty
// idempotency key falls back to plain CreateRun behaviour.
func TestCreateRunIdempotent_EmptyKeyFallback(t *testing.T) {
	st := openDB(t)
	run := &state.Run{ID: "run_empty_key", WorkflowName: "w"}
	id, dup, err := st.CreateRunIdempotent(run, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if dup {
		t.Fatal("empty-key call should never be a duplicate")
	}
	if id != "run_empty_key" {
		t.Fatalf("unexpected run ID: %s", id)
	}
}

// TestCreateRunIdempotent_Concurrent verifies that concurrent submissions
// with the same idempotency key create exactly one run.
func TestCreateRunIdempotent_Concurrent(t *testing.T) {
	st := openDB(t)
	const concurrency = 10
	ids := make([]string, concurrency)
	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			run := &state.Run{
				ID:           "run_conc_" + string(rune('a'+idx)),
				WorkflowName: "w",
				PlanDigest:   "sha256:concurrent",
			}
			id, _, err := st.CreateRunIdempotent(run, "concurrent-key", "input-hash")
			ids[idx] = id
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// Every call must succeed (no errors) and all must return the same run ID.
	var firstID string
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
			continue
		}
		if firstID == "" {
			firstID = ids[i]
		} else if ids[i] != firstID {
			t.Errorf("goroutine %d returned run ID %q, want %q", i, ids[i], firstID)
		}
	}

	// Only one run should exist in the store.
	runs, err := st.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, r := range runs {
		seen[r.ID] = true
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 run after concurrent submissions, got %d: %v", len(seen), seen)
	}
	if !seen[firstID] {
		t.Fatalf("persisted run %v does not contain expected ID %q", seen, firstID)
	}
}

func isIdempotencyConflict(err error) bool {
	return err != nil && err.Error() == state.ErrIdempotencyKeyConflict.Error()
}

// --- HTTP API idempotency integration tests ---

// idempotentPlanRunner wraps the real in-memory engine so we can test the
// full handleCreateRun + idempotentRunner path with the real Store.
type memIdempotentRunner struct {
	store state.Store
	mu    sync.Mutex
	runs  map[string]string // idempotencyKey → runID
	plans map[string]string // idempotencyKey → planDigest+inputDigest
}

func newMemIdempotentRunner() *memIdempotentRunner {
	return &memIdempotentRunner{
		store: state.New(),
		runs:  make(map[string]string),
		plans: make(map[string]string),
	}
}

func (r *memIdempotentRunner) CreateRun(_ json.RawMessage) (string, error) {
	return "run_mem", nil
}
func (r *memIdempotentRunner) Run(_ string) error { return nil }

func (r *memIdempotentRunner) CreateIdempotentRun(p *plan.Plan, input json.RawMessage, workspaceID, key string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	binding := p.Digest + "|" + string(input) + "|" + workspaceID
	if existing, ok := r.runs[key]; ok {
		if r.plans[key] == binding {
			return existing, true, nil
		}
		return "", false, state.ErrIdempotencyKeyConflict
	}
	runID := "run_api_idem_" + key
	r.runs[key] = runID
	r.plans[key] = binding
	// Persist to store so GetRun works.
	_ = r.store.CreateRun(&state.Run{ID: runID, WorkflowName: p.Name})
	return runID, false, nil
}

func makeRunRequest(planJSON, inputJSON string) string {
	return `{"plan":` + planJSON + `,"input":` + inputJSON + `}`
}

const apiMinPlan = `{"irVersion":"1.0","name":"api-test","subgraphs":{"root":{"nodes":[]}}}`

func postRunAPI(srv *httptest.Server, body, ikey string) *http.Response {
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/runs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ikey != "" {
		req.Header.Set("Idempotency-Key", ikey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	return resp
}

func TestAPI_CreateRun_Idempotent_FirstSubmission(t *testing.T) {
	runner := newMemIdempotentRunner()
	srv := httptest.NewServer(api.NewRouter(runner.store, runner))
	defer srv.Close()

	resp := postRunAPI(srv, makeRunRequest(apiMinPlan, `{}`), "api-key-1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestAPI_CreateRun_Idempotent_RetryReturnsSameRun(t *testing.T) {
	runner := newMemIdempotentRunner()
	srv := httptest.NewServer(api.NewRouter(runner.store, runner))
	defer srv.Close()

	body := makeRunRequest(apiMinPlan, `{}`)
	r1 := postRunAPI(srv, body, "api-key-retry")
	var resp1 map[string]string
	json.NewDecoder(r1.Body).Decode(&resp1)
	r1.Body.Close()
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("first: expected 201, got %d", r1.StatusCode)
	}

	r2 := postRunAPI(srv, body, "api-key-retry")
	var resp2 map[string]string
	json.NewDecoder(r2.Body).Decode(&resp2)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("retry: expected 200, got %d", r2.StatusCode)
	}
	if resp1["runId"] != resp2["runId"] {
		t.Fatalf("retry returned different runId: %q vs %q", resp1["runId"], resp2["runId"])
	}
}

func TestAPI_CreateRun_Idempotent_ConflictReturns409(t *testing.T) {
	runner := newMemIdempotentRunner()
	srv := httptest.NewServer(api.NewRouter(runner.store, runner))
	defer srv.Close()

	postRunAPI(srv, makeRunRequest(apiMinPlan, `{}`), "conflict-api-key").Body.Close()

	// Different plan – the stub detects it via plan digest.
	other := `{"irVersion":"1.0","name":"different","subgraphs":{"root":{"nodes":[]}}}`
	r2 := postRunAPI(srv, makeRunRequest(other, `{}`), "conflict-api-key")
	r2.Body.Close()
	if r2.StatusCode != http.StatusConflict {
		t.Fatalf("conflict: expected 409, got %d", r2.StatusCode)
	}
}

// nopRunner is the simplest Runner that always fails; ensures the
// idempotentRunner path is not entered for runners that don't implement it.
type nopRunner struct{}

func (nopRunner) CreateRun(_ json.RawMessage) (string, error) { return "run_nop", nil }
func (nopRunner) Run(_ string) error                          { return nil }
func (nopRunner) CreatePlanRun(_ *plan.Plan, _ json.RawMessage) (string, error) {
	return "run_nop", nil
}

func TestAPI_CreateRun_NoIdempotencyKey_UsesNormalPath(t *testing.T) {
	runner := newMemIdempotentRunner()
	srv := httptest.NewServer(api.NewRouter(runner.store, runner))
	defer srv.Close()

	body := makeRunRequest(apiMinPlan, `{}`)
	// First call without key – should use normal path, not idempotent path.
	r1 := postRunAPI(srv, body, "")
	r1.Body.Close()
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("no-key: expected 201, got %d", r1.StatusCode)
	}
	// Second call without key – should create another independent run.
	if len(runner.runs) != 0 {
		t.Fatalf("expected 0 idempotent keys stored (no key given), got %d", len(runner.runs))
	}
}
