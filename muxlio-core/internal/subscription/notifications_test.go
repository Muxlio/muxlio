package subscription

import (
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestNotificationSelectionAndEscapedLinks(t *testing.T) {
	payload, ok := notificationPayload("run/with?reserved#chars", state.EventRunCancelled)
	if !ok || payload["kind"] != "completion" {
		t.Fatalf("cancelled notification=%+v", payload)
	}
	links := payload["links"].(map[string]any)
	if links["status"] != "/v1/runs/run%2Fwith%3Freserved%23chars/summary" {
		t.Fatalf("links=%+v", links)
	}
	for _, event := range []string{state.EventRunWaiting, state.EventWorkerActivity, state.EventExecutionAttempt} {
		if _, ok := notificationPayload("run", event); ok {
			t.Fatalf("unexpected notification for %s", event)
		}
	}
	sub := Subscription{PayloadMode: "unknown", ID: "notify"}
	if sub.Normalize() == nil {
		t.Fatal("unknown payload mode accepted")
	}
}
