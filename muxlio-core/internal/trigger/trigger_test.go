package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validTrigger(id string, secret string) Trigger {
	return Trigger{
		ID:                id,
		WorkflowDigest:    "sha256:" + id,
		Secret:            secret,
		SignatureScheme:   SchemeHMACSHA256,
		SignatureHeader:   DefaultSignatureHeader,
		SignatureEncoding: EncodingHex,
		Enabled:           true,
	}
}

func signHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func signBase64(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestNormalizeDefaultsAndValidation(t *testing.T) {
	tr := Trigger{ID: "deploy", WorkflowDigest: "sha256:abc", Secret: "0123456789abcdef"}
	if err := tr.Normalize(); err != nil {
		t.Fatal(err)
	}
	if tr.SignatureScheme != SchemeHMACSHA256 || tr.SignatureHeader != DefaultSignatureHeader || tr.SignatureEncoding != EncodingHex {
		t.Fatalf("defaults not applied: %+v", tr)
	}
	if tr.PayloadLimitBytes != DefaultPayloadLimitBytes || tr.DeduplicationSeconds != DefaultDedupSeconds {
		t.Fatalf("limit defaults not applied: %+v", tr)
	}
	// Normalize does not touch Enabled (a bool cannot distinguish an explicit
	// false from a zero value); the create API defaults it when omitted.
	if tr.Enabled {
		t.Fatalf("Normalize must not flip Enabled on")
	}

	for _, bad := range []Trigger{
		{ID: "", WorkflowDigest: "d", Secret: "0123456789abcdef"},
		{ID: "bad id", WorkflowDigest: "d", Secret: "0123456789abcdef"},
		{ID: "x", WorkflowDigest: "", Secret: "0123456789abcdef"},
		{ID: "x", WorkflowDigest: "d", Secret: "short"},
		{ID: "x", WorkflowDigest: "d", Secret: "0123456789abcdef", SignatureScheme: "rsa"},
		{ID: "x", WorkflowDigest: "d", Secret: "0123456789abcdef", SignatureEncoding: "rot13"},
		{ID: "x", WorkflowDigest: "d", Secret: "0123456789abcdef", PayloadLimitBytes: -1},
		{ID: "x", WorkflowDigest: "d", Secret: "0123456789abcdef", PayloadLimitBytes: MaxPayloadLimitBytes + 1},
		{ID: "x", WorkflowDigest: "d", Secret: "0123456789abcdef", DeduplicationSeconds: -1},
		{ID: "x", WorkflowDigest: "d", Secret: "0123456789abcdef", DeduplicationSeconds: MaxDeduplicationSeconds + 1},
		{ID: "x", WorkflowDigest: "d", Secret: "0123456789abcdef", InputMapping: json.RawMessage(`{invalid`)},
	} {
		if err := bad.Normalize(); err == nil {
			t.Fatalf("expected error for %+v", bad)
		}
	}
}

func TestVerifySignature(t *testing.T) {
	tr := validTrigger("deploy", "supersecretkey123456")
	body := []byte(`{"event":"push"}`)
	sig := signHex(tr.Secret, body)

	if err := tr.VerifySignature(body, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// A bad signature must not authenticate.
	if err := tr.VerifySignature(body, "deadbeef"); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("bad signature: got %v want ErrSignatureMismatch", err)
	}
	// A signature over a different body must not authenticate.
	if err := tr.VerifySignature([]byte(`{"event":"tampered"}`), sig); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("tampered body accepted: %v", err)
	}
	// A missing signature header is rejected distinctly.
	if err := tr.VerifySignature(body, ""); !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("missing signature: got %v want ErrSignatureMissing", err)
	}
	// A disabled trigger rejects everything.
	disabled := tr
	disabled.Enabled = false
	if err := disabled.VerifySignature(body, sig); !errors.Is(err, ErrTriggerDisabled) {
		t.Fatalf("disabled trigger: got %v want ErrTriggerDisabled", err)
	}
}

func TestVerifySignaturePrefixAndBase64(t *testing.T) {
	tr := validTrigger("deploy", "supersecretkey123456")
	tr.SignaturePrefix = "sha256="
	body := []byte(`{"ok":true}`)
	sig := "sha256=" + signHex(tr.Secret, body)
	if err := tr.VerifySignature(body, sig); err != nil {
		t.Fatalf("prefixed signature rejected: %v", err)
	}
	// Wrong prefix is a mismatch, not a missing signature.
	if err := tr.VerifySignature(body, "md5="+signHex(tr.Secret, body)); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("wrong prefix: %v", err)
	}

	b64 := validTrigger("deploy", "supersecretkey123456")
	b64.SignatureEncoding = EncodingBase64
	if err := b64.VerifySignature(body, signBase64(b64.Secret, body)); err != nil {
		t.Fatalf("base64 signature rejected: %v", err)
	}
}

func TestMapInputPassthrough(t *testing.T) {
	tr := validTrigger("deploy", "supersecretkey123456")
	payload := []byte(`{"text":"hello","n":3}`)
	out, err := tr.MapInput(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "hello" || got["n"] != float64(3) {
		t.Fatalf("passthrough mismatch: %v", got)
	}
	// Invalid JSON payload fails.
	if _, err := tr.MapInput([]byte(`{bad`)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("invalid payload: %v", err)
	}
}

func TestMapInputTemplate(t *testing.T) {
	tr := validTrigger("deploy", "supersecretkey123456")
	tr.InputMapping = json.RawMessage(`{
		"title": "{{/issue/title}}",
		"action": "{{/action}}",
		"number": "{{/issue/number}}",
		"open": "{{/issue/open}}",
		"summary": "issue {{/issue/number}}: {{/issue/title}} by {{/issue/user/login}}",
		"literal": "static",
		"nested": {"login": "{{/issue/user/login}}"},
		"labels": "{{/issue/labels}}"
	}`)
	payload := []byte(`{"action":"opened","issue":{"number":42,"title":"Fix bug","open":true,"user":{"login":"alice"},"labels":["bug","urgent"]}}`)
	out, err := tr.MapInput(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Fix bug" {
		t.Errorf("title=%v", got["title"])
	}
	if got["action"] != "opened" {
		t.Errorf("action=%v", got["action"])
	}
	if got["number"] != float64(42) {
		t.Errorf("number=%v", got["number"])
	}
	if got["open"] != true {
		t.Errorf("open=%v", got["open"])
	}
	if got["summary"] != "issue 42: Fix bug by alice" {
		t.Errorf("summary=%v", got["summary"])
	}
	if got["literal"] != "static" {
		t.Errorf("literal=%v", got["literal"])
	}
	nested, _ := got["nested"].(map[string]any)
	if nested["login"] != "alice" {
		t.Errorf("nested.login=%v", nested["login"])
	}
	labels, _ := got["labels"].([]any)
	if len(labels) != 2 || labels[0] != "bug" {
		t.Errorf("labels=%v", got["labels"])
	}
}

func TestMapInputMissingReferenceFails(t *testing.T) {
	tr := validTrigger("deploy", "supersecretkey123456")
	tr.InputMapping = json.RawMessage(`{"title":"{{/issue/title}}"}`)
	if _, err := tr.MapInput([]byte(`{"action":"opened"}`)); !errors.Is(err, ErrMappingResolve) {
		t.Fatalf("missing reference: %v", err)
	}
}

func TestMapInputInterpolationRejectsStructuredValue(t *testing.T) {
	tr := validTrigger("deploy", "supersecretkey123456")
	tr.InputMapping = json.RawMessage(`{"summary":"labels: {{/labels}}"}`)
	if _, err := tr.MapInput([]byte(`{"labels":["a","b"]}`)); !errors.Is(err, ErrMappingResolve) {
		t.Fatalf("structured interpolation: %v", err)
	}
}

func TestMappingPreservesLargeNumbers(t *testing.T) {
	tr := validTrigger("numbers", "0123456789abcdef")
	tr.InputMapping = json.RawMessage(`{"id":"{{/id}}","literal":9007199254740993,"text":"id={{/id}}"}`)
	out, err := tr.MapInput([]byte(`{"id":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"id":9007199254740993,"literal":9007199254740993,"text":"id=9007199254740993"}` {
		t.Fatalf("mapping rounded number: %s", out)
	}
}

func TestMappingJSONPointers(t *testing.T) {
	tr := validTrigger("pointer", "0123456789abcdef")
	payload := []byte(`{"a/b":{"~key":[17]}}`)
	tr.InputMapping = json.RawMessage(`"{{/a~1b/~0key/0}}"`)
	out, err := tr.MapInput(payload)
	if err != nil || string(out) != "17" {
		t.Fatalf("escaped pointer: %s %v", out, err)
	}
	for _, mapping := range []string{`"{{/a~2b}}"`, `"{{/a~1b/~0key/00}}"`, `"{{/a~1b/~0key/+0}}"`, `"{{/a~1b/~0key/-}}"`} {
		tr.InputMapping = json.RawMessage(mapping)
		if _, err := tr.MapInput(payload); err == nil {
			t.Fatalf("invalid pointer accepted: %s", mapping)
		}
	}
}

func TestMappingExpansionLimit(t *testing.T) {
	tr := validTrigger("bounded", "0123456789abcdef")
	payload := []byte(`{"text":"` + strings.Repeat("x", 1<<20) + `","object":{"text":"` + strings.Repeat("x", 1<<20) + `"}}`)
	for _, mapping := range []string{
		`"` + strings.Repeat("{{/text}}", 9) + `"`,
		`[` + strings.TrimSuffix(strings.Repeat(`"{{/object}}",`, 9), ",") + `]`,
		`[` + strings.TrimSuffix(strings.Repeat(`"{{/text}}",`, 9), ",") + `]`,
	} {
		tr.InputMapping = json.RawMessage(mapping)
		if _, err := tr.MapInput(payload); !errors.Is(err, ErrMappedInputTooLarge) {
			t.Fatalf("expansion error: %v", err)
		}
	}
	// The cap counts encoded bytes, including escaping, keys and delimiters.
	tr.InputMapping = json.RawMessage(`{"text":"{{/text}}"}`)
	for _, size := range []int{MaxMappedInputBytes - 11, MaxMappedInputBytes - 10} {
		body := []byte(`{"text":"` + strings.Repeat("x", size) + `"}`)
		out, err := tr.MapInput(body)
		if size == MaxMappedInputBytes-11 {
			if err != nil || len(out) != MaxMappedInputBytes {
				t.Fatalf("exact limit: len=%d err=%v", len(out), err)
			}
		} else if !errors.Is(err, ErrMappedInputTooLarge) {
			t.Fatalf("above limit: %v", err)
		}
	}
	tr.InputMapping = json.RawMessage(`"{{/text}}{{/text}}"`)
	escaped, _ := json.Marshal(map[string]string{"text": strings.Repeat("<", 1<<20)})
	if _, err := tr.MapInput(escaped); !errors.Is(err, ErrMappedInputTooLarge) {
		t.Fatalf("escaped expansion: %v", err)
	}
}

func TestMappingLiteralOutsideFloatRange(t *testing.T) {
	tr := validTrigger("numbers", "0123456789abcdef")
	tr.InputMapping = json.RawMessage(`{"literal":1e400}`)
	if err := tr.Normalize(); err != nil {
		t.Fatal(err)
	}
	out, err := tr.MapInput([]byte(`{}`))
	if err != nil || string(out) != `{"literal":1e400}` {
		t.Fatalf("literal changed: %s %v", out, err)
	}
}
