package adapter

import "testing"

func TestCaptureAgentTextAccumulates(t *testing.T) {
	var chunks BoundedBuffer
	CaptureAgentText(&chunks, "hello")
	if chunks.String() != "hello" {
		t.Fatalf("buffer = %q", chunks.String())
	}
}
