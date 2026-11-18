package contract

import (
	"fmt"
	"time"
)

// Availability reasons reported by adapters after a confirmed runtime failure.
// These are distinct from operation success, accounting, and effect/retry safety.
const (
	AvailabilityQuotaExhausted         = "quota_exhausted"
	AvailabilityAuthenticationFailed   = "authentication_failed"
)

// Availability carries a confirmed executor unavailability signal from an
// adapter to readiness and admission. Reason is required when present. RetryAt
// is the known reset time for quota exhaustion; when omitted at record time the
// coordinator applies a default so scheduled recovery remains possible. Auth
// failures omit RetryAt and clear via explicit recheck. Temporary rate limiting
// must not be reported as quota exhaustion.
type Availability struct {
	Reason  string     `json:"reason"`
	Message string     `json:"message,omitempty"`
	RetryAt *time.Time `json:"retryAt,omitempty"`
}

// Validate rejects unknown reasons and empty reports.
func (a *Availability) Validate() error {
	if a == nil {
		return nil
	}
	switch a.Reason {
	case AvailabilityQuotaExhausted, AvailabilityAuthenticationFailed:
	default:
		return fmt.Errorf("unsupported availability reason %q", a.Reason)
	}
	if a.RetryAt != nil && a.RetryAt.IsZero() {
		return fmt.Errorf("availability retryAt must be non-zero when set")
	}
	return nil
}
