package main

import (
	"regexp"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

// classifyAntigravityError maps Antigravity's evidenced failure forms:
//   - structured result error types QuotaError / AuthError
//   - message "quota exceeded" (fixture / typed QuotaError message)
//   - "Individual quota reached … Resets in 130h31m17s" (CLI / RESOURCE_EXHAUSTED)
//   - "Your AI credits balance is too low to continue." / "Out of credits"
//
// Temporary bare rate limits without those quota forms return nil.
func classifyAntigravityError(errType, message string) *contract.Availability {
	typ := strings.ToLower(strings.TrimSpace(errType))
	msg := strings.TrimSpace(message)
	lower := strings.ToLower(typ + " " + msg)

	switch typ {
	case "quotaerror", "quota_exceeded", "quotaexceeded":
		return adapter.QuotaExhausted("provider quota exhausted", parseAntigravityRetryAt(msg, time.Now()))
	case "autherror", "authenticationerror", "unauthorized":
		return adapter.AuthenticationFailed("provider authentication failed")
	}

	if antigravityQuotaMessage(lower) {
		return adapter.QuotaExhausted("provider quota exhausted", parseAntigravityRetryAt(msg, time.Now()))
	}
	if availability := adapter.ClassifyAuthenticationFailure(errType, message); availability != nil {
		return availability
	}
	return nil
}

func antigravityQuotaMessage(lower string) bool {
	for _, marker := range []string{
		"individual quota reached",
		"quota exceeded",
		"your ai credits balance is too low",
		"out of credits",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Antigravity advertises resets as "Resets in 130h31m17s" (optional h/m/s parts).
var antigravityResetsIn = regexp.MustCompile(`(?i)resets?\s+in\s+(\d+h)?(\d+m)?(\d+s)?\b`)

// parseAntigravityRetryAt extracts RetryAt from Antigravity's "Resets in …" form.
// Unknown or missing reset text returns nil so the caller can apply a default.
func parseAntigravityRetryAt(text string, now time.Time) *time.Time {
	m := antigravityResetsIn.FindStringSubmatch(text)
	if len(m) != 4 {
		return nil
	}
	duration, err := time.ParseDuration(strings.ToLower(strings.Join(m[1:], "")))
	if err != nil {
		return nil
	}
	t := now.Add(duration)
	return &t
}
