package contract

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseResultAvailability(t *testing.T) {
	retry := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	raw, _ := json.Marshal(map[string]any{
		"outputs": map[string]any{},
		"error":   "quota exceeded",
		"availability": map[string]any{
			"reason":  AvailabilityQuotaExhausted,
			"message": "provider quota exhausted",
			"retryAt": retry.Format(time.RFC3339Nano),
		},
	})
	result, err := ParseResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Availability == nil || result.Availability.Reason != AvailabilityQuotaExhausted || result.Availability.RetryAt == nil {
		t.Fatalf("availability: %+v", result.Availability)
	}
}

func TestParseResultRejectsUnknownAvailabilityReason(t *testing.T) {
	raw := []byte(`{"outputs":{},"error":"x","availability":{"reason":"rate_limited"}}`)
	if _, err := ParseResult(raw); err == nil {
		t.Fatal("expected unknown reason to fail")
	}
}
