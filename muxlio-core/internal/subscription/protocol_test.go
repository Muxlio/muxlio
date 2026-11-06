package subscription_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/subscription"
)

func TestSignatureAndRecursiveRedaction(t *testing.T) {
	sub := definition("local:audit")
	must(t, sub.Normalize())
	source := map[string]any{
		"nested":                    []map[string]any{{"private": "private-value", "api-key": "key-value", "public": "kept"}},
		"raw":                       json.RawMessage(`{"password":"password-value","public":0}`),
		"Bearer embedded-key-value": "public value",
	}
	record := subscription.Delivery{EventID: "r:1", Payload: source}
	for _, encoding := range []string{subscription.EncodingHex, subscription.EncodingBase64} {
		sub.SignatureEncoding = encoding
		body, sig, err := subscription.BuildEnvelope(sub, record)
		must(t, err)
		for _, secret := range []string{"private-value", "key-value", "password-value", "embedded-key-value"} {
			if bytes.Contains(body, []byte(secret)) {
				t.Fatalf("redaction bypassed: %s", body)
			}
		}
		if !bytes.Contains(body, []byte(`"public":0`)) || !bytes.Contains(body, []byte(`"public":"kept"`)) {
			t.Fatalf("public fields lost: %s", body)
		}
		mac := hmac.New(sha256.New, []byte(sub.Secret))
		mac.Write(body)
		want := hex.EncodeToString(mac.Sum(nil))
		if encoding == subscription.EncodingBase64 {
			want = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		if sig != "sha256="+want {
			t.Fatal("signature does not authenticate exact redacted bytes")
		}
		must(t, sub.VerifyBody(body, sig))
		if sub.VerifyBody(body, "") == nil {
			t.Fatal("unsigned payload accepted")
		}
	}
	// Redaction does not alter the source event.
	raw, err := json.Marshal(source)
	must(t, err)
	if !bytes.Contains(raw, []byte("password-value")) {
		t.Fatal("source mutated")
	}
	sub.Enabled = false
	if _, _, err := subscription.BuildEnvelope(sub, record); err == nil {
		t.Fatal("disabled subscription signed payload")
	}
	sub.Enabled = true
	sub.RedactFields = []string{"eventId"}
	if sub.Normalize() == nil {
		t.Fatal("reserved envelope field accepted")
	}
}

func TestSignatureTransportConfiguration(t *testing.T) {
	for _, header := range []string{"X-Muxlio-Event-ID", "x-muxlio-event-id", "Content-Type", "Content-Length", "Host", "Connection", "Transfer-Encoding", "Trailer", "TE", "Upgrade", "Keep-Alive", "Proxy-Connection"} {
		t.Run(header, func(t *testing.T) {
			sub := definition("https://example.com/hook")
			sub.SignatureHeader = header
			if sub.Normalize() == nil {
				t.Fatal("accepted header that cannot reliably carry a signature")
			}
		})
	}
	for _, prefix := range []string{"sha256=\r\n", "sha256=\x00", "\t", "é", " HMAC "} {
		sub := definition("local:audit")
		sub.SignaturePrefix = prefix
		if sub.Normalize() == nil {
			t.Fatalf("accepted invalid signature prefix %q", prefix)
		}
	}
	sub := definition("https://example.com/hook")
	sub.SignatureHeader = "Authorization"
	sub.SignaturePrefix = "HMAC "
	must(t, sub.Normalize())
}

func TestFiltersAndOversizeEnvelope(t *testing.T) {
	sub := definition("local:audit")
	sub.EventTypes = []string{"worker.*", "run.waiting"}
	must(t, sub.Normalize())
	for _, event := range []string{"worker.activity", "worker.lease.expired", "run.waiting"} {
		if !sub.Matches(event) {
			t.Fatalf("did not match %s", event)
		}
	}
	if sub.Matches("run.succeeded") || sub.Matches("workers.activity") {
		t.Fatal("filter matched unrelated event")
	}
	// diagnostic strings are bounded individually; use many ordinary fields to
	// verify the independent cap on the complete signed body.
	items := make([]string, 2000)
	for i := range items {
		items[i] = strings.Repeat("x", 1024)
	}
	if _, _, err := subscription.BuildEnvelope(sub, subscription.Delivery{Payload: map[string]any{"items": items}}); err == nil {
		t.Fatal("oversize envelope accepted")
	}
}
