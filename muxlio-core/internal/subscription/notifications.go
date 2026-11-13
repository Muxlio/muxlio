package subscription

import (
	"net/url"

	"github.com/muxlio/muxlio-core/internal/state"
)

// notificationPayload deliberately never accepts event payloads. Prompts,
// transcripts, errors, schemas and external operation references must not enter
// notification storage or transport, even when their fields look innocuous.
// Links are relative to the coordinator and carry no credentials.
func notificationPayload(runID, eventType string) (map[string]any, bool) {
	var kind string
	switch eventType {
	case state.EventRunSucceeded, state.EventRunCancelled:
		kind = "completion"
	case state.EventRunFailed:
		kind = "failure"
	case state.EventInputRequired:
		kind = "input_required"
	case state.EventReconciliationRequired:
		kind = "reconciliation_required"
	default:
		return nil, false
	}
	id := url.PathEscape(runID)
	return map[string]any{
		"version": 1,
		"source":  "muxlio.run-event",
		"kind":    kind,
		"links": map[string]any{
			"run":    "/runs/" + id,
			"status": "/v1/runs/" + id + "/summary",
		},
	}, true
}
