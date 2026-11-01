package main

import (
	"strings"
	"testing"
)

func TestTailBufferKeepsLastLimitBytes(t *testing.T) {
	buffer := &tailBuffer{limit: 8}
	_, _ = buffer.Write([]byte("012345"))
	_, _ = buffer.Write([]byte("6789"))
	if got, want := buffer.String(), "23456789"; got != want {
		t.Fatalf("tailBuffer.String() = %q, want %q", got, want)
	}
}

func TestTailBufferTruncatesLargeWrite(t *testing.T) {
	buffer := &tailBuffer{limit: 8}
	_, _ = buffer.Write([]byte("0123456789abcdef"))
	if got, want := buffer.String(), "89abcdef"; got != want {
		t.Fatalf("tailBuffer.String() = %q, want %q", got, want)
	}
}

func TestWithStderr(t *testing.T) {
	tests := []struct {
		name, reason, stderr, want string
	}{
		{name: "detail", reason: "process failed", stderr: " MONTHLY_REQUEST_COUNT\n", want: "process failed (stderr: MONTHLY_REQUEST_COUNT)"},
		{name: "empty stderr", reason: "process failed", want: "process failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withStderr(tt.reason, tt.stderr); got != tt.want {
				t.Fatalf("withStderr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	buffer := &tailBuffer{limit: 8 * 1024}
	_, _ = buffer.Write([]byte(strings.Repeat("x", 9*1024)))
	if got := len(buffer.String()); got != 8*1024 {
		t.Fatalf("stderr tail length = %d, want %d", got, 8*1024)
	}
}
