package state

import (
	"testing"
	"time"
)

func TestDefaultRetentionIsOneDay(t *testing.T) {
	policy := DefaultRetention()
	for _, duration := range []time.Duration{policy.EventHistory, policy.StdoutStderr, policy.Artifacts, policy.Caches, policy.Workspaces} {
		if duration != 24*time.Hour {
			t.Fatal("default retention must be 24 hours")
		}
	}
}
