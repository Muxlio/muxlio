package contract

import (
	"strings"
	"testing"
)

func TestParseProbeResultValid(t *testing.T) {
	raw := []byte(`{"ready":true,"checks":[{"name":"binary","passed":true,"message":"found"}]}`)
	got, err := ParseProbeResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Ready || len(got.Checks) != 1 {
		t.Fatalf("bad parse: %+v", got)
	}
}

func TestParseProbeResultPassedChecksOverrideReadyFalse(t *testing.T) {
	raw := []byte(`{"ready":false,"checks":[{"name":"binary","passed":true,"message":"found"}]}`)
	got, err := ParseProbeResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Ready {
		t.Fatal("all passed checks must make the result ready")
	}
}

func TestParseProbeResultFailedCheckOverridesReady(t *testing.T) {
	// An adapter might claim ready while reporting a failed check; the parser
	// must reconcile so a single missed check is never advertised as ready.
	raw := []byte(`{"ready":true,"checks":[{"name":"authentication","passed":false,"message":"logged out"}]}`)
	got, err := ParseProbeResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Ready {
		t.Fatal("a failed check must make the result not ready")
	}
	if len(got.Failed()) != 1 || got.Failed()[0].Name != ProbeAuthentication {
		t.Fatalf("unexpected failed checks: %v", got.Failed())
	}
}

func TestParseProbeResultRequiresEvidence(t *testing.T) {
	if _, err := ParseProbeResult([]byte(`{}`)); err == nil {
		t.Fatal("empty report accepted")
	}
}

func TestParseProbeResultRejectsNonObject(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `true`, `"x"`, ``} {
		if _, err := ParseProbeResult([]byte(raw)); err == nil {
			t.Errorf("expected error for %q", raw)
		} else if !strings.Contains(err.Error(), "JSON object") {
			t.Errorf("unexpected error for %q: %v", raw, err)
		}
	}
}

func TestProbeResultAdd(t *testing.T) {
	var r ProbeResult
	r.Ready = true
	r.Add(ProbeCheck{Name: ProbeBinary, Passed: true, Message: "ok"})
	if !r.Ready {
		t.Fatal("passing check must keep ready true")
	}
	r.Add(ProbeCheck{Name: ProbeAuthentication, Passed: false, Message: "logged out"})
	if r.Ready {
		t.Fatal("failing check must flip ready to false")
	}
	if len(r.Failed()) != 1 {
		t.Fatalf("expected one failed check, got %v", r.Failed())
	}
}

func TestProbeCheckNamesAreStable(t *testing.T) {
	// These constants are part of the shared wire contract and must not change.
	want := map[string]string{
		"protocol":       ProbeProtocol,
		"binary":         ProbeBinary,
		"version":        ProbeVersion,
		"permission":     ProbePermission,
		"authentication": ProbeAuthentication,
		"backend":        ProbeBackend,
		"tools":          ProbeTools,
	}
	for expected, actual := range want {
		if expected != actual {
			t.Errorf("probe check name mismatch: want %q got %q", expected, actual)
		}
	}
}
