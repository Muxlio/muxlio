package state

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// RetriedInputRequestID identifies the successor of a cancelled input request.
// Old request identities remain closed, including to delayed deliveries.
func RetriedInputRequestID(requestID string) string {
	sum := sha256.Sum256([]byte("input-retry:" + requestID))
	return fmt.Sprintf("req_%x", sum[:16])
}

// RenewCancelledInputs returns fresh requests for cancelled waits belonging to
// incomplete activations. Callers persist these atomically with explicit retry.
// Fulfilled requests and cancelled requests with successors are left intact.
func RenewCancelledInputs(requests []InputRequest, activations map[string]bool, now time.Time) []InputRequest {
	existing := make(map[string]bool, len(requests))
	for _, req := range requests {
		existing[req.RequestID] = true
	}
	var renewed []InputRequest
	for _, req := range requests {
		if req.State != InputRequestCancelled || !activations[req.ActivationID] {
			continue
		}
		nextID := RetriedInputRequestID(req.RequestID)
		if existing[nextID] {
			continue
		}
		next := cloneInputRequest(&req)
		next.RequestID = nextID
		next.State = InputRequestPending
		next.Response = nil
		next.ResponderID = ""
		next.IdempotencyKey = ""
		next.FulfilledAt = nil
		next.CreatedAt = now
		if req.ExpiresAt != nil {
			due := now.Add(req.ExpiresAt.Sub(req.CreatedAt))
			next.ExpiresAt = &due
		}
		renewed = append(renewed, next)
	}
	return renewed
}
