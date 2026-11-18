package main

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestClassifyAntigravityTypedQuotaAndAuth(t *testing.T) {
	quota := classifyAntigravityError("QuotaError", "quota exceeded")
	if quota == nil || quota.Reason != contract.AvailabilityQuotaExhausted || quota.RetryAt != nil {
		t.Fatalf("typed QuotaError: %+v", quota)
	}
	auth := classifyAntigravityError("AuthError", "auth failed")
	if auth == nil || auth.Reason != contract.AvailabilityAuthenticationFailed {
		t.Fatalf("typed AuthError: %+v", auth)
	}
}

func TestClassifyAntigravityIndividualQuotaResetsIn(t *testing.T) {
	// Evidenced CLI / RESOURCE_EXHAUSTED form from Antigravity issues.
	msg := "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 6h0m0s."
	now := time.Now()
	got := classifyAntigravityError("QuotaError", msg)
	if got == nil || got.Reason != contract.AvailabilityQuotaExhausted {
		t.Fatalf("individual quota: %+v", got)
	}
	if got.RetryAt == nil {
		t.Fatal("expected RetryAt from Resets in")
	}
	delta := got.RetryAt.Sub(now)
	if delta < 5*time.Hour+50*time.Minute || delta > 6*time.Hour+10*time.Minute {
		t.Fatalf("RetryAt delta=%v want ~6h", delta)
	}
}

func TestClassifyAntigravityResourceExhaustedMessage(t *testing.T) {
	msg := "RESOURCE_EXHAUSTED (code 429): Individual quota reached. Contact your administrator to enable overages. Resets in 167h39m40s."
	got := classifyAntigravityError("", msg)
	if got == nil || got.Reason != contract.AvailabilityQuotaExhausted || got.RetryAt == nil {
		t.Fatalf("resource exhausted: %+v", got)
	}
}

func TestClassifyAntigravityIgnoresBareRateLimit(t *testing.T) {
	if got := classifyAntigravityError("", "HTTP 429 too many requests"); got != nil {
		t.Fatalf("bare rate limit must not become quota: %+v", got)
	}
}

func TestParseAntigravityRetryAt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := parseAntigravityRetryAt("Resets in 130h31m17s.", now)
	want := now.Add(130*time.Hour + 31*time.Minute + 17*time.Second)
	if got == nil || !got.Equal(want) {
		t.Fatalf("got %+v want %v", got, want)
	}
	if parseAntigravityRetryAt("no reset advertised", now) != nil {
		t.Fatal("missing Resets in must return nil")
	}
}
