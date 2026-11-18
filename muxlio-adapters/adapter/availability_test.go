package adapter

import (
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestClassifyAuthenticationFailure(t *testing.T) {
	auth := ClassifyAuthenticationFailure("failed", "auth failed")
	if auth == nil || auth.Reason != contract.AvailabilityAuthenticationFailed {
		t.Fatalf("auth: %+v", auth)
	}
	loggedOut := ClassifyAuthenticationFailure("probe", "not logged in")
	if loggedOut == nil || loggedOut.Reason != contract.AvailabilityAuthenticationFailed {
		t.Fatalf("login: %+v", loggedOut)
	}
	if got := ClassifyAuthenticationFailure("failed", "model overloaded"); got != nil {
		t.Fatalf("unknown must not invent availability: %+v", got)
	}
	if got := ClassifyAuthenticationFailure("failed", "quota exceeded"); got != nil {
		t.Fatalf("generic runners must not guess quota: %+v", got)
	}
}
