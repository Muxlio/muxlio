//go:build !unix

package conformance_test

import (
	"os"
	"testing"
)

func TestNativeAuthorizationLive(t *testing.T) {
	if os.Getenv("MUXLIO_LIVE_AUTHORIZATION") == "" {
		t.Skip("live authorization probes require explicit selection")
	}
	t.Fatal("live authorization probes require Unix process-group containment")
}
