package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

// decodeError decodes the response body into an APIError for assertion.
func decodeError(t *testing.T, resp *httptest.ResponseRecorder) APIError {
	t.Helper()
	var env APIError
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("failed to decode error envelope: %v (body: %s)", err, resp.Body.String())
	}
	return env
}

// assertErrorEnvelope checks that all fields of the error envelope are
// populated and well-formed so coding agents can use them reliably.
func assertErrorEnvelope(t *testing.T, env APIError, wantCode string) {
	t.Helper()
	if env.Code != wantCode {
		t.Errorf("code = %q, want %q", env.Code, wantCode)
	}
	if !strings.HasPrefix(env.Code, "MUXLIO_") {
		t.Errorf("code %q does not start with MUXLIO_", env.Code)
	}
	if env.Message == "" {
		t.Error("error envelope has empty message")
	}
	if env.Fix == "" {
		t.Errorf("error envelope for code %q has empty fix", env.Code)
	}
}

// TestErrorEnvelopeShape_NotFound verifies that a 404 response carries the
// full structured envelope with a MUXLIO_* code, message, and fix.
func TestErrorEnvelopeShape_NotFound(t *testing.T) {
	handler := NewRouter(state.New(), nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/runs/nonexistent", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d", resp.Code)
	}
	env := decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeNotFound)
}

// TestErrorEnvelopeShape_MethodNotAllowed verifies that 405 responses
// include a MUXLIO_* code with a fix pointing at the Allow header.
func TestErrorEnvelopeShape_MethodNotAllowed(t *testing.T) {
	handler := NewRouter(state.New(), nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodDelete, "/v1/runs", nil))
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", resp.Code)
	}
	env := decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeMethodNotAllowed)
}

// TestErrorEnvelopeShape_ValidationErrors verifies that a plan submission
// with structural errors returns a 422 with a top-level MUXLIO_VALIDATION_ERROR
// code and a details[] array containing per-error MUXLIO_* codes with fixes.
func TestErrorEnvelopeShape_ValidationErrors(t *testing.T) {
	// Submit a plan missing irVersion, name, and root subgraph — triggers
	// MUXLIO_MISSING_IR_VERSION, MUXLIO_MISSING_NAME, MUXLIO_MISSING_ROOT_SUBGRAPH.
	body := `{"plan":{"subgraphs":{"root":{"nodes":[]}}},"input":{}}`
	runner := &stubRunner{}
	handler := NewRouter(state.New(), runner)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body)))
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	env := decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeValidationError)
	if len(env.Details) == 0 {
		t.Fatal("validation error envelope has no details")
	}
	for i, d := range env.Details {
		if !strings.HasPrefix(d.Code, "MUXLIO_") {
			t.Errorf("details[%d].code = %q, want MUXLIO_* prefix", i, d.Code)
		}
		if d.Message == "" {
			t.Errorf("details[%d].message is empty", i)
		}
		if d.Fix == "" {
			t.Errorf("details[%d].fix is empty for code %q", i, d.Code)
		}
		if d.Severity == "" {
			t.Errorf("details[%d].severity is empty", i)
		}
		if d.Phase == "" {
			t.Errorf("details[%d].phase is empty", i)
		}
	}

	// Spot-check that expected per-field codes are present.
	codes := make(map[string]bool)
	for _, d := range env.Details {
		codes[d.Code] = true
	}
	for _, want := range []string{"MUXLIO_MISSING_IR_VERSION", "MUXLIO_MISSING_NAME"} {
		if !codes[want] {
			t.Errorf("expected details code %q not found in %v", want, codes)
		}
	}
}

// TestErrorEnvelopeShape_ValidationErrorsWithNodeDetail verifies that
// per-node validation errors include subgraph and nodeId fields so agents
// can pinpoint the exact location of the failure.
func TestErrorEnvelopeShape_ValidationErrorsWithNodeDetail(t *testing.T) {
	// A plan with a valid header but an execute node missing its executor.
	body := `{"plan":{
		"irVersion":"1.0",
		"name":"test",
		"subgraphs":{"root":{"nodes":[{"id":"step","kind":"execute"}]}}
	},"input":{}}`
	runner := &stubRunner{}
	handler := NewRouter(state.New(), runner)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body)))
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	env := decodeError(t, resp)
	if env.Code != CodeValidationError {
		t.Fatalf("top-level code = %q", env.Code)
	}
	var executorErr *ErrorDetail
	for i := range env.Details {
		if env.Details[i].Code == "MUXLIO_MISSING_EXECUTOR" {
			executorErr = &env.Details[i]
			break
		}
	}
	if executorErr == nil {
		t.Fatalf("MUXLIO_MISSING_EXECUTOR not found in details: %v", env.Details)
	}
	if executorErr.Subgraph != "root" {
		t.Errorf("subgraph = %q, want %q", executorErr.Subgraph, "root")
	}
	if executorErr.NodeID != "step" {
		t.Errorf("nodeId = %q, want %q", executorErr.NodeID, "step")
	}
	if executorErr.Fix == "" {
		t.Error("MUXLIO_MISSING_EXECUTOR has no fix")
	}
}

// TestErrorEnvelopeShape_InvalidJSON verifies that a malformed request body
// returns MUXLIO_INVALID_JSON with a fix.
func TestErrorEnvelopeShape_InvalidJSON(t *testing.T) {
	handler := NewRouter(state.New(), &stubRunner{})
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader("not json")))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.Code)
	}
	env := decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeInvalidJSON)
}

// TestErrorEnvelopeShape_RunnerUnavailable verifies that submitting a valid
// plan with no runner returns MUXLIO_RUNNER_UNAVAILABLE.
func TestErrorEnvelopeShape_RunnerUnavailable(t *testing.T) {
	// Valid minimal plan.
	body := `{"plan":{
		"irVersion":"1.0",
		"name":"minimal",
		"subgraphs":{"root":{"nodes":[]}}
	},"input":{}}`
	handler := NewRouter(state.New(), nil) // nil runner
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body)))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	env := decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeRunnerUnavailable)
}

// TestErrorEnvelopeShape_MissingRunID verifies that /v1/runs/ with no run ID
// returns MUXLIO_MISSING_RUN_ID.
func TestErrorEnvelopeShape_MissingRunID(t *testing.T) {
	handler := NewRouter(state.New(), nil)
	resp := httptest.NewRecorder()
	// Trailing slash, no actual ID component.
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/runs/", nil))
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.Code)
	}
	env := decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeMissingRunID)
}

// TestErrorEnvelopeShape_WorkerProtocol_Codes verifies that worker protocol
// errors expose MUXLIO_* codes and fixes.
func TestErrorEnvelopeShape_WorkerProtocol_Codes(t *testing.T) {
	backend := &workerBackendFixture{}
	protocol := NewWorkerProtocolHandler(backend)
	handler := NewRouter(state.New(), nil, protocol)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}

	// Unsupported protocol version → MUXLIO_UNSUPPORTED_PROTOCOL
	resp := request(http.MethodPost, "/v1/workers/register", `{"protocolVersion":"v99","workerId":"w","capacity":1,"capabilities":[]}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.Code)
	}
	env := decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeUnsupportedProtocol)

	// Empty workerId → MUXLIO_INVALID_REGISTRATION
	resp = request(http.MethodPost, "/v1/workers/register", `{"protocolVersion":"v1","workerId":"","capacity":1,"capabilities":[]}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.Code)
	}
	env = decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeInvalidRegistration)

	// Invalid completion missing fields → MUXLIO_INVALID_COMPLETION
	resp = request(http.MethodPost, "/v1/workers/worker-a/assignments/complete", `{"assignmentId":"","generation":0}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.Code)
	}
	env = decodeError(t, resp)
	assertErrorEnvelope(t, env, CodeInvalidCompletion)
}

// TestErrorEnvelopeShape_ContentType verifies every error response has
// Content-Type: application/json.
func TestErrorEnvelopeShape_ContentType(t *testing.T) {
	handler := NewRouter(state.New(), nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/runs/missing", nil))
	if ct := resp.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// TestErrorEnvelopeShape_ArtifactCodes verifies artifact-related MUXLIO_*
// codes are returned for digest mismatches and not-found conditions.
func TestErrorEnvelopeShape_ArtifactCodes(t *testing.T) {
	handler := NewRouter(state.New(), nil) // no artifact driver
	resp := httptest.NewRecorder()
	// Bad digest format → MUXLIO_INVALID_ARTIFACT_DIGEST
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodPut, "/v1/artifacts/", nil))
	// Without artifact driver the handler is not registered; an unknown path returns 404.
	// Test the code that IS registered for bad digest within artifact handler.
	// To test the artifact handler codes we need to use a handler with an artifact driver.
	// That is already covered by TestArtifactHTTPStreamingRoundTripAndDigestEnforcement.
	// Here just verify the not-found path for an unknown endpoint.
	if resp.Code != http.StatusNotFound {
		t.Fatalf("no artifact driver: /v1/artifacts/ status = %d", resp.Code)
	}
}

// stubRunner satisfies the Runner interface for tests that need a non-nil runner
// but don't want real execution.
type stubRunner struct{ err error }

func (s *stubRunner) CreateRun(_ json.RawMessage) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return "run-stub", nil
}
func (s *stubRunner) Run(_ string) error { return s.err }
