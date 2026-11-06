package api

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/plan"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiagnosticDetailsPreserveContractAndRedact(t *testing.T) {
	raw := `{"irVersion":"1.0","name":"repair","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/task","inputs":{"x":{"ref":{"node":"token=private-value","port":"x"}}}}]}}}`
	var p plan.Plan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	issues := plan.Validate(&p)
	resp := httptest.NewRecorder()
	writeValidationErrors(resp, issues)
	env := decodeError(t, resp)
	if env.ID == "" || len(env.Details) != 1 || env.Details[0].ID != issues[0].ID || env.Details[0].Source.Pointer != issues[0].Source.Pointer {
		t.Fatalf("lost diagnostic contract: %+v", env)
	}
	if strings.Contains(resp.Body.String(), "private-value") {
		t.Fatal("validation error leaked credential")
	}
}

func TestValidationEnvelopeIsBounded(t *testing.T) {
	issues := make([]plan.ValidationError, 100)
	for i := range issues {
		issues[i] = plan.ValidationError{ID: "diag_test", Code: "MUXLIO_TEST", Message: strings.Repeat("<", 10000), Fix: "repair"}
	}
	response := httptest.NewRecorder()
	writeValidationErrors(response, issues)
	env := decodeError(t, response)
	if len(response.Body.Bytes()) > 64*1024 || env.Code != CodeValidationError || len(env.Details) == 0 {
		t.Fatalf("invalid bounded envelope: bytes=%d", response.Body.Len())
	}
}
