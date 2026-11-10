package diagnostic

import (
	"strings"
	"testing"
)

func TestRedactEnvSecretsDoNotEraseUnrelatedErrorText(t *testing.T) {
	for _, tc := range []struct {
		value, msg, need string
	}{
		{"host", "dial tcp 10.0.0.1:8080: connect: no route to host", "no route to host"},
		{"permitted", "open /tmp/plan.json: operation not permitted", "operation not permitted"},
	} {
		t.Run(tc.need, func(t *testing.T) {
			t.Setenv("TEST_API_TOKEN", tc.value)
			got := Redact(tc.msg)
			if !strings.Contains(got, tc.need) {
				t.Fatalf("env value %q corrupted diagnostic: %s", tc.value, got)
			}
		})
	}
}

func TestRedactPreservesNetworkDiagnostics(t *testing.T) {
	cases := []string{
		"dial tcp 192.168.1.1:8080: connect: no route to host",
		"open /etc/shadow: operation not permitted",
	}
	for _, msg := range cases {
		if got := Redact(msg); got != msg {
			t.Fatalf("changed network diagnostic:\n in:  %s\n out: %s", msg, got)
		}
	}
}

func TestRedactStillScrubsLongEnvCredentials(t *testing.T) {
	t.Setenv("TEST_API_TOKEN", "environment-credential")
	msg := "request failed: environment-credential"
	got := Redact(msg)
	if strings.Contains(got, "environment-credential") {
		t.Fatalf("long env credential leaked: %s", got)
	}
}
