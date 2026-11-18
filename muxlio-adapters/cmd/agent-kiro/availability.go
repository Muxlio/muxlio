package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

// kiroQuotaAvailability reads the ACP error data embedded in a terminal CLI
// failure. The generic RPC code is shared with other provider failures.
func kiroQuotaAvailability(failure string) *contract.Availability {
	_, data, ok := strings.Cut(failure, " (code -32000): ")
	if !ok {
		return nil
	}
	var payload struct {
		ErrorType string          `json:"errorType"`
		ResetAt   json.RawMessage `json:"resetAt"`
	}
	if json.Unmarshal([]byte(data), &payload) != nil || payload.ErrorType != "UsageLimitReachedError" {
		return nil
	}
	var retryAt *time.Time
	var reset time.Time
	if json.Unmarshal(payload.ResetAt, &reset) == nil && reset.After(time.Now()) {
		retryAt = &reset
	}
	return adapter.QuotaExhausted("You've reached your monthly usage limit.", retryAt)
}
