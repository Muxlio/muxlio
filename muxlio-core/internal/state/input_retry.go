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

// RenewClosedInputs returns fresh requests for cancelled or expired waits
// belonging to incomplete activations. Callers persist these atomically with
// explicit retry. Successors are only followed from cancelled requests, so the
// expired predecessors returned in superseded must be closed as cancelled in
// the same transaction. Fulfilled requests and requests with successors are
// left intact.
func RenewClosedInputs(requests []InputRequest, activations map[string]bool, now time.Time) (renewed []InputRequest, superseded []string) {
	existing := make(map[string]bool, len(requests))
	for _, req := range requests {
		existing[req.RequestID] = true
	}
	for _, req := range requests {
		expired := req.State == InputRequestExpired || (req.State == InputRequestPending && req.ExpiresAt != nil && !req.ExpiresAt.After(now))
		if (req.State != InputRequestCancelled && !expired) || !activations[req.ActivationID] {
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
		if expired {
			superseded = append(superseded, req.RequestID)
		}
	}
	return renewed, superseded
}
