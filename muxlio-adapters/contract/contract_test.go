package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEffectCapabilitiesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		request    Request
		capability string
	}{
		{"key", Request{Effect: "idempotent-with-key", IdempotencyKey: "operation-1"}, CapabilityIdempotencyKey},
		{"reconciliation", Request{Action: "reconcile", OperationAttemptID: "attempt-1", RemoteTask: &RemoteTaskReference{ID: "external-1"}}, CapabilityReconcile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &AdapterManifest{}
			if err := tc.request.Check(manifest); err == nil {
				t.Fatal("unsupported effect contract accepted")
			}
			manifest.Capabilities = map[string]bool{tc.capability: true}
			if err := tc.request.Check(manifest); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, effect := range []string{"idempotency-key", "idempotent-with-key"} {
		if err := (&Request{Effect: effect}).Check(&AdapterManifest{Capabilities: map[string]bool{CapabilityIdempotencyKey: true}}); err == nil {
			t.Fatal("keyed effect accepted without a key")
		}
	}
	if err := (&Request{Action: "submit-again"}).Check(&AdapterManifest{}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestParseResultRejectsMalformedCompletion(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `[]`, `true`, `{broken`, `{} {}`, `{} trailing`,
		`{"outputs":[]}`, `{"usage":{"input":-1}}`,
		`{"usage":{"output":-1}}`, `{"usage":{"cached":-1}}`,
		`{"executionDurationMs":-1}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseResult([]byte(raw)); err == nil {
				t.Fatalf("accepted malformed result %s", raw)
			}
		})
	}
	for _, raw := range []string{`{}`, `{"usage":null}`, `{"usage":{"input":0,"output":0,"cached":0}}`} {
		result, err := ParseResult([]byte(raw))
		if err != nil || (result.Usage != nil) != strings.Contains(raw, `"input"`) {
			t.Fatalf("usage availability changed: %+v, %v", result, err)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `true`, `{broken`, `{"contractVersion":"99"}`, `{"manifestVersion":"99"}`, `{"capabilities":{"progress":"yes"}}`} {
		if _, err := ParseManifest([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestExactOptionalRevisions(t *testing.T) {
	m := &AdapterManifest{Executor: "test", ExecutorVersion: "1.2.3", ContractVersion: Version, Capabilities: map[string]bool{CapabilityProgress: true}, CapabilityVersions: map[string]string{CapabilityProgress: "2"}}
	for _, tc := range []struct {
		executor, implementation, protocol, capability, revision string
		ok                                                       bool
	}{
		{"test", "1.2.3", "1", CapabilityProgress, "2", true},
		{"test", "1.2.3", "1", CapabilityProgress, "1", false},
		{"test", "1.2.3", "1", CapabilitySession, "1", false},
		{"other", "1.2.3", "1", CapabilityProgress, "2", false},
		{"test", "9", "1", CapabilityProgress, "2", false},
		{"test", "1.2.3", "9", CapabilityProgress, "2", false},
	} {
		err := m.Check(tc.executor, tc.implementation, tc.protocol, map[string]string{tc.capability: tc.revision})
		if (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestUsageAvailabilitySurvivesWireRoundTrip(t *testing.T) {
	for _, usage := range []*TokenUsage{nil, {}, {Input: 10}} {
		raw, err := json.Marshal(Result{Usage: usage})
		if err != nil {
			t.Fatal(err)
		}
		var result Result
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if (result.Usage == nil) != (usage == nil) || (usage != nil && !result.Usage.Equal(*usage)) {
			t.Fatalf("usage lost: %s", raw)
		}
	}
}

func TestImplicitRequirementsDoNotMutateCaller(t *testing.T) {
	r := Request{Executor: "test", SessionID: "existing", RequiredCapabilities: map[string]string{CapabilityProgress: Version}}
	err := r.Check(&AdapterManifest{Capabilities: map[string]bool{CapabilityProgress: true}})
	if err == nil || !strings.Contains(err.Error(), CapabilitySession) {
		t.Fatalf("session silently emulated: %v", err)
	}
	if len(r.RequiredCapabilities) != 1 {
		t.Fatal("caller requirements mutated")
	}
}

func TestImplicitRequirementsPreserveExplicitRevisions(t *testing.T) {
	for _, capability := range []string{CapabilitySession, CapabilityRemoteTask, CapabilityArtifacts} {
		t.Run(capability, func(t *testing.T) {
			m := &AdapterManifest{Capabilities: map[string]bool{capability: true}}
			r := Request{Executor: "test", RequiredCapabilities: map[string]string{capability: "99"}}
			switch capability {
			case CapabilitySession:
				r.SessionID = "existing"
			case CapabilityRemoteTask:
				r.RemoteTask = &RemoteTaskReference{ID: "existing"}
			case CapabilityArtifacts:
				r.Outputs = map[string]OutputDecl{"report": {Path: "report"}}
			}
			if err := r.Check(m); err == nil || !strings.Contains(err.Error(), "99") {
				t.Fatalf("explicit revision was ignored: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Executor contract: per-capability lifecycle tests
// ---------------------------------------------------------------------------

// TestUnsupportedCapabilityErrorSurface verifies that every optional capability
// returns a typed UnsupportedCapabilityError when absent from the manifest.
func TestUnsupportedCapabilityErrorSurface(t *testing.T) {
	for _, capability := range []string{
		CapabilityCancel, CapabilityProgress, CapabilityArtifacts,
		CapabilityTypedResult, CapabilityUsage, CapabilitySession, CapabilityRemoteTask,
	} {
		t.Run(capability, func(t *testing.T) {
			m := &AdapterManifest{Executor: "test"}
			err := m.Check("test", "", "", map[string]string{capability: Version})
			if err == nil {
				t.Fatal("expected error for unsupported capability")
			}
			var unsupported *UnsupportedCapabilityError
			if !errors.As(err, &unsupported) {
				t.Fatalf("expected UnsupportedCapabilityError, got %T: %v", err, err)
			}
			if unsupported.Capability != capability {
				t.Errorf("Capability = %q, want %q", unsupported.Capability, capability)
			}
			if unsupported.Executor != "test" {
				t.Errorf("Executor = %q, want %q", unsupported.Executor, "test")
			}
			if !strings.Contains(unsupported.Error(), capability) {
				t.Errorf("error message must contain capability name: %q", unsupported.Error())
			}
			if !strings.Contains(unsupported.Error(), "test") {
				t.Errorf("error message must contain executor name: %q", unsupported.Error())
			}
		})
	}
}

// TestUnsupportedCapabilityErrorWithoutExecutor verifies the error message
// when no executor name is provided (e.g. during manifest-only validation).
func TestUnsupportedCapabilityErrorWithoutExecutor(t *testing.T) {
	e := &UnsupportedCapabilityError{Capability: CapabilitySession}
	if !strings.Contains(e.Error(), CapabilitySession) {
		t.Errorf("error must contain capability name: %q", e.Error())
	}
	if strings.Contains(e.Error(), `""`) {
		t.Errorf("empty executor should not appear in error: %q", e.Error())
	}
}

// TestSupportsNilReceiver ensures a nil manifest never panics and always
// returns false for any capability.
func TestSupportsNilReceiver(t *testing.T) {
	var m *AdapterManifest
	for _, capability := range []string{
		CapabilityCancel, CapabilityProgress, CapabilityArtifacts,
		CapabilityTypedResult, CapabilityUsage, CapabilitySession, CapabilityRemoteTask,
	} {
		if m.Supports(capability) {
			t.Errorf("nil manifest must not claim %q", capability)
		}
	}
}

// TestParseManifestSafeDefaults verifies that missing or empty input returns a
// safe zero-value manifest with no capabilities declared.
func TestParseManifestSafeDefaults(t *testing.T) {
	for _, raw := range [][]byte{nil, {}} {
		m, err := ParseManifest(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m == nil {
			t.Fatal("expected non-nil manifest for empty input")
		}
		for _, capability := range []string{
			CapabilityCancel, CapabilityProgress, CapabilityArtifacts,
			CapabilityTypedResult, CapabilityUsage, CapabilitySession, CapabilityRemoteTask,
		} {
			if m.Supports(capability) {
				t.Errorf("safe-default manifest must not claim %q", capability)
			}
		}
	}
}

// TestRequestCheckCancelCapability verifies that requesting the cancel
// capability is gated by the manifest declaration.
func TestRequestCheckCancelCapability(t *testing.T) {
	without := &AdapterManifest{Executor: "test"}
	with := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilityCancel: true}}

	req := Request{
		Executor:             "test",
		RequiredCapabilities: map[string]string{CapabilityCancel: Version},
	}
	if err := req.Check(without); err == nil {
		t.Error("cancel accepted without capability declaration")
	}
	if err := req.Check(with); err != nil {
		t.Errorf("cancel rejected when declared: %v", err)
	}
}

// TestRequestCheckProgressCapability verifies progress capability gating.
func TestRequestCheckProgressCapability(t *testing.T) {
	without := &AdapterManifest{Executor: "test"}
	with := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilityProgress: true}}
	req := Request{Executor: "test", RequiredCapabilities: map[string]string{CapabilityProgress: Version}}
	if err := req.Check(without); err == nil {
		t.Error("progress accepted without capability declaration")
	}
	if err := req.Check(with); err != nil {
		t.Errorf("progress rejected when declared: %v", err)
	}
}

// TestRequestCheckTypedResultCapability verifies typedResult capability gating.
func TestRequestCheckTypedResultCapability(t *testing.T) {
	without := &AdapterManifest{Executor: "test"}
	with := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilityTypedResult: true}}
	req := Request{Executor: "test", RequiredCapabilities: map[string]string{CapabilityTypedResult: Version}}
	if err := req.Check(without); err == nil {
		t.Error("typedResult accepted without capability declaration")
	}
	if err := req.Check(with); err != nil {
		t.Errorf("typedResult rejected when declared: %v", err)
	}
}

// TestRequestCheckUsageCapability verifies usage capability gating.
func TestRequestCheckUsageCapability(t *testing.T) {
	without := &AdapterManifest{Executor: "test"}
	with := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilityUsage: true}}
	req := Request{Executor: "test", RequiredCapabilities: map[string]string{CapabilityUsage: Version}}
	if err := req.Check(without); err == nil {
		t.Error("usage accepted without capability declaration")
	}
	if err := req.Check(with); err != nil {
		t.Errorf("usage rejected when declared: %v", err)
	}
}

// TestRequestCheckSessionCapability verifies that SessionID in the request
// implicitly requires the session capability, and is not emulated when absent.
func TestRequestCheckSessionCapability(t *testing.T) {
	without := &AdapterManifest{Executor: "test"}
	with := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilitySession: true}}

	// Implicit via SessionID field.
	req := Request{Executor: "test", SessionID: "sess-1"}
	err := req.Check(without)
	if err == nil {
		t.Error("session implicitly emulated without capability")
	}
	var unsupported *UnsupportedCapabilityError
	if !errors.As(err, &unsupported) || unsupported.Capability != CapabilitySession {
		t.Errorf("expected UnsupportedCapabilityError for session, got %v", err)
	}
	if err := req.Check(with); err != nil {
		t.Errorf("session rejected when declared: %v", err)
	}

	// Implicit via legacy params.sessionId.
	legacyReq := Request{Executor: "test", Params: json.RawMessage(`{"sessionId":"existing"}`)}
	if err := legacyReq.Check(without); err == nil {
		t.Error("legacy params.sessionId implicitly emulated")
	}
	if err := legacyReq.Check(with); err != nil {
		t.Errorf("legacy params.sessionId rejected when declared: %v", err)
	}
}

// TestRequestCheckRemoteTaskCapability verifies that RemoteTask in the request
// implicitly requires the remoteTask capability, and is not emulated when absent.
func TestRequestCheckRemoteTaskCapability(t *testing.T) {
	without := &AdapterManifest{Executor: "test"}
	with := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilityRemoteTask: true}}

	req := Request{Executor: "test", RemoteTask: &RemoteTaskReference{ID: "task-123"}}
	err := req.Check(without)
	if err == nil {
		t.Error("remoteTask implicitly emulated without capability")
	}
	var unsupported *UnsupportedCapabilityError
	if !errors.As(err, &unsupported) || unsupported.Capability != CapabilityRemoteTask {
		t.Errorf("expected UnsupportedCapabilityError for remoteTask, got %v", err)
	}
	if err := req.Check(with); err != nil {
		t.Errorf("remoteTask rejected when declared: %v", err)
	}
}

// TestRequestCheckArtifactsCapability verifies that a file-path output port
// implicitly requires the artifacts capability, and is not emulated when absent.
func TestRequestCheckArtifactsCapability(t *testing.T) {
	without := &AdapterManifest{Executor: "test"}
	with := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilityArtifacts: true}}

	req := Request{Executor: "test", Outputs: map[string]OutputDecl{"report": {Path: "report.md", Mime: "text/markdown"}}}
	err := req.Check(without)
	if err == nil {
		t.Error("artifacts implicitly emulated without capability")
	}
	if !strings.Contains(err.Error(), CapabilityArtifacts) {
		t.Errorf("error must mention artifacts capability: %v", err)
	}
	if err := req.Check(with); err != nil {
		t.Errorf("artifacts rejected when declared: %v", err)
	}

	// Scalar output ports never require the artifacts capability.
	scalarReq := Request{Executor: "test", Outputs: map[string]OutputDecl{"value": {Mime: "application/json"}}}
	if err := scalarReq.Check(without); err != nil {
		t.Errorf("scalar output must not require artifacts: %v", err)
	}
}

// TestCompatibilityCheckedBeforeDispatch verifies that Check rejects mismatched
// contract and executor versions before any invocation can proceed.
// This mirrors the coordinator's pre-dispatch validation.
func TestCompatibilityCheckedBeforeDispatch(t *testing.T) {
	m := &AdapterManifest{
		ManifestVersion: ManifestVersion,
		ContractVersion: Version,
		Executor:        "agent/test",
		ExecutorVersion: "1",
		Capabilities:    map[string]bool{CapabilityProgress: true},
	}
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{"wrong contract version", Request{Executor: "agent/test", ContractVersion: "99"}},
		{"wrong executor version", Request{Executor: "agent/test", ExecutorVersion: "99"}},
		{"wrong executor name", Request{Executor: "other/test"}},
		{"wrong capability version", Request{Executor: "agent/test", RequiredCapabilities: map[string]string{CapabilityProgress: "99"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.Check(m); err == nil {
				t.Fatal("incompatible request accepted before dispatch")
			}
		})
	}
	// A compatible request must be accepted.
	compatible := Request{
		Executor:             "agent/test",
		ExecutorVersion:      "1",
		ContractVersion:      Version,
		RequiredCapabilities: map[string]string{CapabilityProgress: Version},
	}
	if err := compatible.Check(m); err != nil {
		t.Fatalf("compatible request rejected: %v", err)
	}
}

// TestTokenUsageNilDistinctFromZero verifies the nil-vs-zero invariant: a nil
// pointer means the provider did not report usage; a non-nil zero value is a
// reported zero. Neither may be estimated from credits or context-window data.
func TestTokenUsageNilDistinctFromZero(t *testing.T) {
	// Round-trip nil.
	var nilUsage *TokenUsage
	raw, _ := json.Marshal(Result{Outputs: map[string]any{}, Usage: nilUsage})
	if strings.Contains(string(raw), `"usage"`) {
		t.Errorf("nil usage must be omitted from JSON, got: %s", raw)
	}

	// Round-trip reported zero.
	zeroUsage := &TokenUsage{}
	raw, _ = json.Marshal(Result{Outputs: map[string]any{}, Usage: zeroUsage})
	if !strings.Contains(string(raw), `"usage"`) {
		t.Errorf("reported zero must be present in JSON, got: %s", raw)
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || !result.Usage.Equal(TokenUsage{}) {
		t.Errorf("reported zero not recovered: %+v", result.Usage)
	}

	// Round-trip populated usage.
	filled := &TokenUsage{Input: 100, Output: 50, Cached: 25}
	raw, _ = json.Marshal(Result{Outputs: map[string]any{}, Usage: filled})
	var result2 Result
	if err := json.Unmarshal(raw, &result2); err != nil {
		t.Fatal(err)
	}
	if result2.Usage == nil || !result2.Usage.Equal(*filled) {
		t.Errorf("usage values not recovered: %+v", result2.Usage)
	}
}

// TestRemoteTaskReferenceRoundTrip verifies that RemoteTaskReference survives
// JSON serialization with both ID and optional URL.
func TestRemoteTaskReferenceRoundTrip(t *testing.T) {
	for _, ref := range []RemoteTaskReference{
		{ID: "task-abc"},
		{ID: "task-xyz", URL: "https://provider.example/tasks/task-xyz"},
	} {
		raw, err := json.Marshal(Result{Outputs: map[string]any{}, RemoteTask: &ref})
		if err != nil {
			t.Fatal(err)
		}
		var result Result
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.RemoteTask == nil || *result.RemoteTask != ref {
			t.Errorf("RemoteTask = %+v, want %+v", result.RemoteTask, ref)
		}
	}
	// Nil RemoteTask must be omitted.
	raw, _ := json.Marshal(Result{Outputs: map[string]any{}})
	if strings.Contains(string(raw), `"remoteTask"`) {
		t.Errorf("nil RemoteTask must be omitted, got: %s", raw)
	}
}

// TestProgressEventRequiresNonEmptyStream verifies the Progress type carries
// a non-empty stream identifier (e.g. "agent", "stderr").
func TestProgressEventRequiresNonEmptyStream(t *testing.T) {
	// A valid progress event has a non-empty stream.
	valid := Progress{Stream: "agent", Text: "doing work…"}
	raw, _ := json.Marshal(valid)
	var decoded Progress
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Stream == "" {
		t.Fatalf("valid progress lost: %s", raw)
	}

	// An event with an empty stream is detectable (stream == "").
	empty := Progress{Text: "missing stream"}
	raw, _ = json.Marshal(empty)
	var decodedEmpty Progress
	if err := json.Unmarshal(raw, &decodedEmpty); err != nil {
		t.Fatal(err)
	}
	if decodedEmpty.Stream != "" {
		t.Fatal("unexpected non-empty stream")
	}
}

func TestOptionalMeasurementsDoNotInventTokens(t *testing.T) {
	result, err := ParseResult([]byte(`{"measurements":{"estimatedTokens":42,"costUsd":0,"estimatedCostUsd":0.25}}`))
	if err != nil || result.Usage != nil || result.Measurements == nil || *result.Measurements.CostUSD != 0 || *result.Measurements.EstimatedTokens != 42 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, raw := range []string{`{"measurements":{"costUsd":-1}}`, `{"measurements":{"estimatedTokens":-1}}`, `{"measurements":{"estimatedCostUsd":-1}}`} {
		if _, err := ParseResult([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
