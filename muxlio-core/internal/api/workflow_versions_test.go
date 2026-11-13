package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestWorkflowVersionsAPI(t *testing.T) {
	st := state.New()
	router := NewRouter(st, nil)

	p := &plan.Plan{
		Name:      "test-wf",
		IRVersion: "1.0",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "n1",
						Kind:     plan.KindExecute,
						Executor: "agent/claude",
					},
				},
			},
		},
	}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	p.Digest = digest
	planRaw, _ := json.Marshal(p)

	// 1. Upload workflow version
	postReq := httptest.NewRequest(http.MethodPost, "/v1/workflow-versions", bytes.NewReader(planRaw))
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", postRec.Code, postRec.Body.String())
	}

	var postResp map[string]string
	_ = json.Unmarshal(postRec.Body.Bytes(), &postResp)
	if postResp["digest"] != digest {
		t.Errorf("expected digest %s, got %s", digest, postResp["digest"])
	}

	// 2. Fetch workflow version by digest
	getReq := httptest.NewRequest(http.MethodGet, "/v1/workflow-versions/"+digest, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var fetched plan.Plan
	if err := json.Unmarshal(getRec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("failed to decode fetched plan: %v", err)
	}
	if fetched.Digest != digest || fetched.Name != "test-wf" {
		t.Errorf("fetched plan mismatch: %+v", fetched)
	}

	// 3. Nonexistent digest returns 404
	missingReq := httptest.NewRequest(http.MethodGet, "/v1/workflow-versions/nonexistent", nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing version, got %d", missingRec.Code)
	}
}

// Invalid content must not reserve an immutable version's digest.
func TestWorkflowVersionRegistrationVerifiesAndNormalizesPin(t *testing.T) {
	st := state.New()
	router := NewRouter(st, nil)
	p := &plan.Plan{Name: "valid", IRVersion: "1.0", Root: "root", Subgraphs: map[string]plan.Subgraph{
		"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/echo"}}},
	}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	post := func(raw []byte, status int) {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/workflow-versions", bytes.NewReader(raw)))
		if rec.Code != status {
			t.Fatalf("registration: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	p.Name = "tampered"
	raw, _ := json.Marshal(p)
	post(raw, http.StatusBadRequest)
	if _, err := st.GetWorkflowVersion(digest); err == nil {
		t.Fatal("invalid content reserved a digest")
	}
	p.Name = "valid"
	p.Digest = ""
	raw, _ = json.Marshal(p)
	post(raw, http.StatusCreated)
	stored, err := st.GetWorkflowVersion(digest)
	if err != nil {
		t.Fatal(err)
	}
	var pinned plan.Plan
	if err := json.Unmarshal(stored, &pinned); err != nil {
		t.Fatal(err)
	}
	if pinned.Digest != digest {
		t.Fatal("registry omitted the computed digest")
	}
	if err := pinned.VerifyDigest(); err != nil {
		t.Fatal(err)
	}
	p.Digest = digest
	raw, _ = json.MarshalIndent(p, "", "  ")
	post(raw, http.StatusCreated)
}

func TestRegisteredSubmissionRejectsInvalidRequests(t *testing.T) {
	st := state.New()
	router := NewRouter(st, nil)
	raw := []byte(`{"irVersion":"1.0","name":"typed","root":"root","inputSchema":{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"additionalProperties":false},"subgraphs":{"root":{"nodes":[]}}}`)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workflow-versions", bytes.NewReader(raw)))
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var version map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	digest := version["digest"]
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"missing", `{"planDigest":"sha256:missing","input":{}}`, 404, CodeNotFound},
		{"neither", `{"input":{}}`, 400, CodeInvalidJSON},
		{"both", `{"planDigest":"` + digest + `","plan":{},"input":{}}`, 400, CodeInvalidJSON},
		{"wrong type", `{"planDigest":"` + digest + `","input":{"count":"one"}}`, 400, plan.InputValidationFailed},
		{"missing input", `{"planDigest":"` + digest + `"}`, 400, plan.InputValidationFailed},
		{"extra input", `{"planDigest":"` + digest + `","input":{"count":1,"extra":true}}`, 400, plan.InputValidationFailed},
		{"null input", `{"planDigest":"` + digest + `","input":null}`, 400, plan.InputValidationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := postRun(router, tc.body, "key")
			if got.Code != tc.status || decodeError(t, got).Code != tc.code {
				t.Fatalf("%d %s", got.Code, got.Body.String())
			}
		})
	}
	for i := 0; i < 2; i++ {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("DELETE", "/v1/workflow-versions/"+digest, nil))
		if w.Code != 200 {
			t.Fatalf("retire: %d %s", w.Code, w.Body.String())
		}
	}
	// Uploading the same bytes must never reactivate a retired pin.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workflow-versions", bytes.NewReader(raw)))
	if w.Code != 201 {
		t.Fatalf("register again: %d %s", w.Code, w.Body.String())
	}
	got := postRun(router, `{"planDigest":"`+digest+`","input":{"count":1}}`, "key")
	if got.Code != 410 || decodeError(t, got).Code != CodeWorkflowVersionRetired {
		t.Fatalf("retired: %d %s", got.Code, got.Body.String())
	}
	if _, err := st.GetWorkflowVersion(digest); !errors.Is(err, state.ErrWorkflowVersionRetired) {
		t.Fatalf("retired lookup: %v", err)
	}
	versions, err := st.ListWorkflowVersions()
	if err != nil || len(versions) != 1 || !versions[0].Retired {
		t.Fatalf("list: %+v %v", versions, err)
	}
	// Changed content registers under a different digest.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workflow-versions", strings.NewReader(strings.Replace(string(raw), "typed", "changed", 1))))
	if w.Code != 201 {
		t.Fatalf("changed: %d %s", w.Code, w.Body.String())
	}
	var changed map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &changed)
	if changed["digest"] == digest {
		t.Fatal("changed definition reused original digest")
	}
}

func TestRegisteredSubmissionRejectsCorruptPin(t *testing.T) {
	st := state.New()
	if err := st.SaveWorkflowVersion("sha256:wrong", []byte(minimalPlanJSON)); err != nil {
		t.Fatal(err)
	}
	w := postRun(NewRouter(st, nil), `{"planDigest":"sha256:wrong","input":{}}`, "")
	if w.Code != 500 || decodeError(t, w).Code != CodeStoreError {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// Exercise reference resolution and caller scoping through the authenticated
// handler with a generic runner, without depending on an execution adapter.
type registeredStubRunner struct {
	store state.Store
}

func (r *registeredStubRunner) CreateRun(json.RawMessage) (string, error) {
	return "", errors.New("registered submission must not fall back to CreateRun")
}

func (r *registeredStubRunner) Run(string) error { return nil }

func (r *registeredStubRunner) CreateRegisteredRun(raw []byte, input json.RawMessage, workspace, key string) (string, bool, error) {
	engine := plan.NewEngineWithStore(&plan.Plan{}, nil, r.store)
	return engine.CreateRegisteredRun(raw, input, workspace, key, fmt.Sprintf("%x", sha256.Sum256(input)))
}

func TestRegisteredSubmissionScopesRetriesToAuthenticatedCaller(t *testing.T) {
	st := state.New()
	router := NewRouter(st, &registeredStubRunner{store: st})
	clients := []http.Handler{WithAuth(router, "first-client", "worker"), WithAuth(router, "second-client", "worker")}
	request := func(client int, method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+[]string{"first-client", "second-client"}[client])
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		clients[client].ServeHTTP(w, req)
		return w
	}
	registered := request(0, "POST", "/v1/workflow-versions", `{"irVersion":"1.0","name":"shared","root":"root","inputSchema":{"type":"object","required":["n"],"properties":{"n":{"type":"integer"}}},"subgraphs":{"root":{"nodes":[]}}}`, "")
	if registered.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", registered.Code, registered.Body.String())
	}
	var version map[string]string
	if err := json.Unmarshal(registered.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	pinned, err := st.GetWorkflowVersion(version["digest"])
	if err != nil {
		t.Fatal(err)
	}
	body := `{"planDigest":"` + version["digest"] + `","workspaceId":"project","input":{"n":9007199254740993}}`
	ids := make([]string, len(clients))
	for client := range clients {
		for attempt, status := range []int{http.StatusCreated, http.StatusOK} {
			w := request(client, "POST", "/v1/runs", body, "shared-key")
			if w.Code != status {
				t.Fatalf("client %d attempt %d: %d %s", client, attempt, w.Code, w.Body.String())
			}
			var response map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if attempt == 0 {
				ids[client] = response["runId"]
			} else if ids[client] != response["runId"] {
				t.Fatal("retry created another run")
			}
		}
		run, err := st.GetRun(ids[client])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(run.PlanJSON, pinned) || run.PlanDigest != version["digest"] || run.WorkspaceID != "project" {
			t.Fatalf("pin or target changed: %+v", run)
		}
		if run.Input.(map[string]any)["n"].(json.Number).String() != "9007199254740993" {
			t.Fatalf("input rounded: %v", run.Input)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("independent authenticated callers shared an idempotency binding")
	}
}

func TestRegisteredSubmissionRequiresRunnerSupport(t *testing.T) {
	st := state.New()
	router := NewRouter(st, newIdempotentStubRunner())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workflow-versions", strings.NewReader(minimalPlanJSON)))
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var version map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	w = postRun(router, `{"planDigest":"`+version["digest"]+`","input":{}}`, "key")
	if w.Code != http.StatusServiceUnavailable || decodeError(t, w).Code != CodeRunnerUnavailable {
		t.Fatalf("unsupported runner: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkflowVersionDigestBindsExactSchemaNumbers(t *testing.T) {
	st := state.New()
	router := NewRouter(st, &registeredStubRunner{store: st})
	var previous string
	for _, number := range []string{"9007199254740992", "9007199254740993"} {
		raw := `{"irVersion":"1.0","name":"precise","root":"root","inputSchema":{"const":` + number + `},"subgraphs":{"root":{"nodes":[]}}}`
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workflow-versions", strings.NewReader(raw)))
		if w.Code != http.StatusCreated {
			t.Fatalf("register %s: %d %s", number, w.Code, w.Body.String())
		}
		var version map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &version); err != nil {
			t.Fatal(err)
		}
		digest := version["digest"]
		if digest == "" || digest == previous {
			t.Fatalf("distinct schema numbers shared digest %q", digest)
		}
		if previous != "" {
			// A change to only the low integer bit must invalidate the old digest.
			stale := strings.Replace(raw, `"name":"precise"`, `"name":"precise","digest":"`+previous+`"`, 1)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("POST", "/v1/workflow-versions", strings.NewReader(stale)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("accepted stale numeric digest: %d %s", w.Code, w.Body.String())
			}
		}
		w = postRun(router, `{"planDigest":"`+digest+`","input":`+number+`}`, "")
		if w.Code != http.StatusCreated {
			t.Fatalf("start precise version: %d %s", w.Code, w.Body.String())
		}
		previous = digest
	}
}
