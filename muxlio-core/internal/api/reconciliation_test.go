package api

import (
	"github.com/muxlio/muxlio-core/internal/state"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperatorCanInspectAndResolveUncertainAttempt(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "act", State: "awaiting_reconciliation", Effect: "nonretryable", AwaitReconciliationReason: "worker lost after external effect; verify the external ledger"}); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(s, nil)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("GET", "/v1/runs/run/attempts", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "verify the external ledger") {
		t.Fatalf("hold hidden: %d %s", response.Code, response.Body.String())
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"outcome":"not-applied"}`, 409},
		{`{"outcome":"completed","reason":"checked"}`, 409},
		{`{"outcome":"not-applied","reason":"operator confirmed old operation cannot commit"}`, 200},
	} {
		response = httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("POST", "/v1/runs/run/attempts/attempt/resolve", strings.NewReader(tc.body)))
		if response.Code != tc.status {
			t.Fatalf("resolve %s: %d %s", tc.body, response.Code, response.Body.String())
		}
	}
	attempts, _ := s.ListExecutionAttempts("run", "")
	if len(attempts) != 1 || attempts[0].State != "resolved" || attempts[0].AwaitReconciliationReason != "" {
		t.Fatalf("hold not resolved: %+v", attempts)
	}
}
