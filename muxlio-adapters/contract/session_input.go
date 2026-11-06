package contract

import "fmt"

const (
	SessionInputVersion = "1"
	// CapabilitySessionInput promises durable request-ID deduplication across
	// live and boundary deliveries, and acknowledgment only after application.
	// The same ID and payload must never be applied twice, including after a
	// lost receipt or adapter restart. Conflicting payloads must be rejected.
	CapabilitySessionInput = "sessionInput"
	// CapabilityLiveInput additionally permits Action=steer while a resumed
	// turn is active. The adapter owns the transport and verifies session state;
	// returning success alone never acknowledges delivery.
	CapabilityLiveInput = "liveInput"
)

type SessionInput struct {
	Version   string `json:"version"`
	RequestID string `json:"requestId"`
	Message   string `json:"message"`
}

func (r *Request) CheckSessionInputs() error {
	if len(r.SessionInputs) == 0 {
		if r.Action == "steer" {
			return fmt.Errorf("steer requires session input")
		}
		return nil
	}
	if r.Session == nil || r.Session.Mode != "resume" {
		return fmt.Errorf("input delivery requires an existing session")
	}
	seen := make(map[string]bool, len(r.SessionInputs))
	for _, input := range r.SessionInputs {
		if input.Version != SessionInputVersion || input.RequestID == "" || input.Message == "" {
			return fmt.Errorf("invalid session input version, request identity or message")
		}
		if seen[input.RequestID] {
			return fmt.Errorf("duplicate session input request identity")
		}
		seen[input.RequestID] = true
	}
	return nil
}
