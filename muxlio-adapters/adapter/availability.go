package adapter

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

// QuotaExhausted builds a confirmed quota-exhaustion availability signal.
func QuotaExhausted(message string, retryAt *time.Time) *contract.Availability {
	if strings.TrimSpace(message) == "" {
		message = "provider quota exhausted"
	}
	return &contract.Availability{
		Reason:  contract.AvailabilityQuotaExhausted,
		Message: message,
		RetryAt: retryAt,
	}
}

// AuthenticationFailed builds a confirmed authentication-failure signal.
func AuthenticationFailed(message string) *contract.Availability {
	if strings.TrimSpace(message) == "" {
		message = "provider authentication failed"
	}
	return &contract.Availability{
		Reason:  contract.AvailabilityAuthenticationFailed,
		Message: message,
	}
}

// ClassifyAuthenticationFailure maps runner failure text to authentication
// unavailability using markers already shared by adapter probes and fixtures
// ("not logged in", "auth failed", …). It does not invent quota signals.
func ClassifyAuthenticationFailure(reason, diagnostics string) *contract.Availability {
	text := strings.ToLower(reason + " " + diagnostics)
	for _, marker := range []string{
		"not logged in", "logged out", "unauthenticated",
		"auth failed", "authentication failed", "login required",
		`"loggedin":false`, `"loggedin": false`,
	} {
		if strings.Contains(text, marker) {
			return AuthenticationFailed("provider authentication failed")
		}
	}
	return nil
}

// FailWithAvailability writes an error Response that also carries a confirmed
// availability signal for admission, then exits 0.
func FailWithAvailability(snapshot *contract.Accounting, availability *contract.Availability, format string, args ...any) {
	_ = WriteResponse(Response{
		Outputs:      map[string]any{},
		Error:        fmt.Sprintf(format, args...),
		Accounting:   snapshot,
		Availability: availability,
	})
	os.Exit(0)
}

// FailRunner reports a runner failure, attaching authentication availability
// when diagnostics match shared login-failure markers. Provider-specific quota
// classification belongs in the owning adapter.
func FailRunner(snapshot *contract.Accounting, reason, diagnostics, login string) {
	message := RunnerFailure(reason, diagnostics, login)
	if availability := ClassifyAuthenticationFailure(reason, diagnostics); availability != nil {
		FailWithAvailability(snapshot, availability, "%s", message)
	}
	FailWithAccounting(snapshot, "%s", message)
}
