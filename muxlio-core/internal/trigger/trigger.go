// Package trigger defines inbound webhook triggers that start pinned runs from
// authenticated, idempotent deliveries.
//
// A trigger pins an immutable, content-addressed workflow version and owns the
// secret used to verify inbound event signatures. External systems (GitHub,
// Slack, product webhooks, or anything that can sign a payload) deliver events
// to the coordinator without the coordinator learning any provider-specific
// protocol: verification is a generic HMAC over the raw body, the payload is
// mapped to the workflow's typed input through a small JSON-pointer template,
// and duplicate deliveries within a deduplication window collapse to a single
// run. This keeps the coordinator a generic trigger receiver rather than a
// library of product-specific event handlers.
package trigger

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Configuration defaults and bounds. A trigger without explicit limits still
// enforces safe defaults so a misconfigured trigger cannot admit an unbounded
// payload or remember an idempotency key forever.
const (
	SchemeHMACSHA256         = "hmac-sha256"
	DefaultSignatureHeader   = "X-Muxlio-Signature"
	EncodingHex              = "hex"
	EncodingBase64           = "base64"
	DefaultPayloadLimitBytes = 1 << 20 // 1 MiB
	DefaultDedupSeconds      = 24 * 60 * 60

	MinPayloadLimitBytes    = 1
	MaxMappedInputBytes     = 8 << 20 // independent bound on mapping expansion
	MaxPayloadLimitBytes    = 8 << 20
	MinDeduplicationSeconds = 1
	MaxDeduplicationSeconds = 30 * 24 * 60 * 60
	MinSecretBytes          = 16
	MaxIDLen                = 128
)

// Delivery states recorded for a trigger.
const (
	DeliveryDelivered = "delivered"
	DeliveryRejected  = "rejected"
	DeliveryDuplicate = "duplicate"
)

// Sentinel errors. Handlers map these to stable HTTP error codes.
var (
	ErrTriggerDisabled     = errors.New("muxlio: trigger is disabled")
	ErrInvalidConfig       = errors.New("muxlio: trigger has an invalid signature configuration")
	ErrSignatureMissing    = errors.New("muxlio: signature header is missing")
	ErrSignatureMismatch   = errors.New("muxlio: signature does not match the request body")
	ErrInvalidPayload      = errors.New("muxlio: webhook payload is not valid JSON")
	ErrMappedInputTooLarge = errors.New("muxlio: mapped input exceeds 8 MiB limit")
	ErrInvalidMapping      = errors.New("muxlio: input mapping is invalid")
	ErrMappingResolve      = errors.New("muxlio: input mapping could not resolve a payload reference")
)

// Trigger is an immutable definition that pins a workflow version and
// authenticates inbound webhook deliveries. The Secret is the only field that
// is never serialized to API responses (json:"-"); the store retains it so the
// delivery handler can verify signatures.
type Trigger struct {
	ID                   string          `json:"id"`
	WorkflowDigest       string          `json:"workflowDigest"`
	Secret               string          `json:"-"` // HMAC secret; never serialized
	SignatureScheme      string          `json:"signatureScheme"`
	SignatureHeader      string          `json:"signatureHeader"`
	SignaturePrefix      string          `json:"signaturePrefix,omitempty"`
	SignatureEncoding    string          `json:"signatureEncoding,omitempty"`
	InputMapping         json.RawMessage `json:"inputMapping,omitempty"`
	PayloadLimitBytes    int             `json:"payloadLimitBytes"`
	DeduplicationSeconds int             `json:"deduplicationSeconds"`
	Enabled              bool            `json:"enabled"`
}

// Summary is a trigger without its secret, suitable for list/get responses.
// Because Secret carries json:"-", marshaling a Trigger already omits it; this
// alias documents the intent and keeps the surface stable.
type Summary = Trigger

// Delivery records one inbound webhook decision for a trigger.
type Delivery struct {
	TriggerID      string    `json:"triggerId"`
	IdempotencyKey string    `json:"idempotencyKey,omitempty"`
	WorkflowDigest string    `json:"workflowDigest"`
	RunID          string    `json:"runId,omitempty"`
	State          string    `json:"state"`
	Reason         string    `json:"reason,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

// WebhookDeliveryRequest carries the validated, pinned material the store needs
// to atomically deduplicate and create a run. The API layer verifies the
// signature, maps the payload, validates the typed input against the plan
// schema, and supplies the registered immutable plan bytes so the resulting run
// records exactly which plan was used.
type WebhookDeliveryRequest struct {
	TriggerID            string
	IdempotencyKey       string
	WorkflowDigest       string
	WorkflowName         string
	PlanJSON             []byte
	Input                json.RawMessage
	DeduplicationSeconds int
}

// WebhookDeliveryResult is the outcome of an atomic delivery. Duplicate is true
// when an earlier delivery with the same idempotency key within the
// deduplication window already created a run; RunID is that run.
type WebhookDeliveryResult struct {
	RunID          string
	WorkflowDigest string
	Duplicate      bool
}

// Store persists triggers and their delivery history and atomically deduplicates
// inbound deliveries. Only durable stores implement it; the in-memory store
// does not, so the API reports trigger storage unavailable when it is absent
// (matching the schedule store contract).
type Store interface {
	CreateTrigger(Trigger) error
	GetTrigger(id string) (Trigger, error)
	ListTriggers() ([]Trigger, error)
	DeliverWebhook(req WebhookDeliveryRequest) (WebhookDeliveryResult, error)
	RecordDelivery(d Delivery) error
	ListDeliveries(triggerID string) ([]Delivery, error)
}

// Normalize applies safe defaults and validates the definition. It mutates the
// receiver so the caller can store the normalized form. Enabled is preserved; the creation API applies its own default.
func (t *Trigger) Normalize() error {
	if t == nil {
		return errors.New("muxlio: trigger is nil")
	}
	if t.ID == "" || len(t.ID) > MaxIDLen {
		return fmt.Errorf("muxlio: id must contain 1..%d characters", MaxIDLen)
	}
	for _, c := range t.ID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errors.New("muxlio: id must use letters, digits, - or _")
		}
	}
	if t.WorkflowDigest == "" {
		return errors.New("muxlio: workflowDigest must reference a registered immutable workflow version")
	}
	if len([]byte(t.Secret)) < MinSecretBytes {
		return fmt.Errorf("muxlio: secret must be at least %d bytes", MinSecretBytes)
	}
	switch t.SignatureScheme {
	case "":
		t.SignatureScheme = SchemeHMACSHA256
	case SchemeHMACSHA256:
	default:
		return fmt.Errorf("muxlio: unsupported signature scheme %q", t.SignatureScheme)
	}
	if t.SignatureHeader == "" {
		t.SignatureHeader = DefaultSignatureHeader
	}
	for _, c := range t.SignatureHeader {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return errors.New("muxlio: signatureHeader must be a valid HTTP header name")
		}
	}
	switch t.SignatureEncoding {
	case "":
		t.SignatureEncoding = EncodingHex
	case EncodingHex, EncodingBase64:
	default:
		return fmt.Errorf("muxlio: unsupported signature encoding %q", t.SignatureEncoding)
	}
	if t.PayloadLimitBytes == 0 {
		t.PayloadLimitBytes = DefaultPayloadLimitBytes
	}
	if t.PayloadLimitBytes < MinPayloadLimitBytes || t.PayloadLimitBytes > MaxPayloadLimitBytes {
		return fmt.Errorf("muxlio: payloadLimitBytes must be between %d and %d", MinPayloadLimitBytes, MaxPayloadLimitBytes)
	}
	if t.DeduplicationSeconds == 0 {
		t.DeduplicationSeconds = DefaultDedupSeconds
	}
	if t.DeduplicationSeconds < MinDeduplicationSeconds || t.DeduplicationSeconds > MaxDeduplicationSeconds {
		return fmt.Errorf("muxlio: deduplicationSeconds must be between %d and %d", MinDeduplicationSeconds, MaxDeduplicationSeconds)
	}
	if len(t.InputMapping) > 0 {
		if !json.Valid(t.InputMapping) {
			return ErrInvalidMapping
		}
	}
	return nil
}

// VerifySignature authenticates a raw webhook body against the supplied header
// value using the trigger's secret and signature configuration. Comparison is
// constant-time. A disabled trigger rejects every delivery.
func (t *Trigger) VerifySignature(body []byte, headerValue string) error {
	if t == nil || !t.Enabled {
		return ErrTriggerDisabled
	}
	if len([]byte(t.Secret)) < MinSecretBytes || t.SignatureScheme != SchemeHMACSHA256 {
		return ErrInvalidConfig
	}
	sig := headerValue
	if t.SignaturePrefix != "" {
		if !strings.HasPrefix(sig, t.SignaturePrefix) {
			return ErrSignatureMismatch
		}
		sig = strings.TrimPrefix(sig, t.SignaturePrefix)
	}
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return ErrSignatureMissing
	}
	var want []byte
	var err error
	switch t.SignatureEncoding {
	case "", EncodingHex:
		want, err = hex.DecodeString(sig)
	case EncodingBase64:
		want, err = base64.StdEncoding.DecodeString(sig)
	default:
		return ErrInvalidConfig
	}
	if err != nil {
		return ErrSignatureMismatch
	}
	mac := hmac.New(sha256.New, []byte(t.Secret))
	mac.Write(body)
	if subtle.ConstantTimeCompare(want, mac.Sum(nil)) != 1 {
		return ErrSignatureMismatch
	}
	return nil
}

// MapInput maps the webhook payload to the run input using the trigger's input
// mapping. With no mapping the payload is used verbatim (it must be valid
// JSON). A mapping is a JSON template: string leaf values may embed
// {{/json-pointer}} references that are resolved against the payload. A string
// that is exactly one reference is replaced by the referenced value (any JSON
// type); a string with embedded references interpolates each scalar value into
// the surrounding literal text. Other JSON values are literals.
func (t *Trigger) MapInput(payload []byte) (json.RawMessage, error) {
	if len(t.InputMapping) == 0 {
		if !json.Valid(payload) {
			return nil, ErrInvalidPayload
		}
		return payload, nil
	}
	var payloadVal any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if !json.Valid(payload) {
		return nil, ErrInvalidPayload
	}
	if err := dec.Decode(&payloadVal); err != nil {
		return nil, ErrInvalidPayload
	}
	var template any
	mappingDecoder := json.NewDecoder(bytes.NewReader(t.InputMapping))
	mappingDecoder.UseNumber()
	if err := mappingDecoder.Decode(&template); err != nil {
		return nil, ErrInvalidMapping
	}
	remaining := MaxMappedInputBytes
	mapped, err := resolveTemplate(template, payloadVal, &remaining)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(mapped)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveTemplate recursively resolves {{/pointer}} references in string leaves.
func resolveTemplate(tmpl any, payload any, remaining *int) (any, error) {
	switch v := tmpl.(type) {
	case string:
		resolved, err := resolveStringTemplate(v, payload, *remaining)
		if err != nil {
			return nil, err
		}
		if err := chargeJSON(resolved, remaining); err != nil {
			return nil, err
		}
		return resolved, nil
	case map[string]any:
		if err := chargeBytes(2, remaining); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(v))
		for k, val := range v {
			if len(out) > 0 {
				if err := chargeBytes(1, remaining); err != nil {
					return nil, err
				}
			}
			if err := chargeJSON(k, remaining); err != nil {
				return nil, err
			}
			if err := chargeBytes(1, remaining); err != nil {
				return nil, err
			}
			resolved, err := resolveTemplate(val, payload, remaining)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	case []any:
		if err := chargeBytes(2, remaining); err != nil {
			return nil, err
		}
		out := make([]any, len(v))
		for i, val := range v {
			if i > 0 {
				if err := chargeBytes(1, remaining); err != nil {
					return nil, err
				}
			}
			resolved, err := resolveTemplate(val, payload, remaining)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		if err := chargeJSON(tmpl, remaining); err != nil {
			return nil, err
		}
		return tmpl, nil
	}
}

// Account for each occurrence of a referenced value before encoding the result.
// Walking structured references avoids allocating their expanded JSON first.
func chargeBytes(n int, remaining *int) error {
	if n > *remaining {
		return ErrMappedInputTooLarge
	}
	*remaining -= n
	return nil
}

func chargeJSON(value any, remaining *int) error {
	switch v := value.(type) {
	case map[string]any:
		if err := chargeBytes(2, remaining); err != nil {
			return err
		}
		first := true
		for key, item := range v {
			if !first {
				if err := chargeBytes(1, remaining); err != nil {
					return err
				}
			}
			first = false
			if err := chargeJSON(key, remaining); err != nil {
				return err
			}
			if err := chargeBytes(1, remaining); err != nil {
				return err
			}
			if err := chargeJSON(item, remaining); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if err := chargeBytes(2, remaining); err != nil {
			return err
		}
		for i, item := range v {
			if i > 0 {
				if err := chargeBytes(1, remaining); err != nil {
					return err
				}
			}
			if err := chargeJSON(item, remaining); err != nil {
				return err
			}
		}
		return nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return chargeBytes(len(raw), remaining)
	}
}

func resolveStringTemplate(s string, payload any, remaining int) (any, error) {
	// A whole-string single reference yields the referenced value verbatim,
	// preserving its JSON type (object, array, number, bool, string, null).
	if strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") && strings.Count(s, "{{") == 1 && strings.Count(s, "}}") == 1 {
		inner := strings.TrimSpace(s[2 : len(s)-2])
		if strings.HasPrefix(inner, "/") || inner == "" {
			return resolvePointer(payload, inner)
		}
	}
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	// Interpolation: replace each {{/pointer}} with the referenced scalar
	// rendered as a string. References into structured values are rejected so
	// the resulting string is unambiguous.
	var b strings.Builder
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '{' && s[i+1] == '{' {
			end := strings.Index(s[i+2:], "}}")
			if end < 0 {
				return nil, ErrInvalidMapping
			}
			inner := strings.TrimSpace(s[i+2 : i+2+end])
			val, err := resolvePointer(payload, inner)
			if err != nil {
				return nil, err
			}
			rendered, err := scalarToString(val)
			if err != nil {
				return nil, err
			}
			if len(rendered) > remaining-b.Len() {
				return nil, ErrMappedInputTooLarge
			}
			b.WriteString(rendered)
			i += 2 + end + 2
			continue
		}
		if b.Len() >= remaining {
			return nil, ErrMappedInputTooLarge
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), nil
}

func scalarToString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		return x.String(), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("%w: reference resolves to a non-scalar value", ErrMappingResolve)
	}
}

// resolvePointer evaluates an RFC 6901 JSON pointer against a decoded payload.
// An empty pointer returns the whole document.
func resolvePointer(payload any, ptr string) (any, error) {
	if ptr == "" {
		return payload, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("%w: pointer %q must start with '/'", ErrMappingResolve, ptr)
	}
	cur := payload
	tokens := strings.Split(ptr[1:], "/")
	for _, raw := range tokens {
		for i := 0; i < len(raw); i++ {
			if raw[i] == '~' {
				if i+1 == len(raw) || (raw[i+1] != '0' && raw[i+1] != '1') {
					return nil, fmt.Errorf("%w: invalid pointer escape", ErrMappingResolve)
				}
				i++
			}
		}
		token := strings.ReplaceAll(raw, "~1", "/")
		token = strings.ReplaceAll(token, "~0", "~")
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[token]
			if !ok {
				return nil, fmt.Errorf("%w: no key %q at pointer %q", ErrMappingResolve, token, ptr)
			}
			cur = next
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') || strings.IndexFunc(token, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
				return nil, fmt.Errorf("%w: invalid array index %q", ErrMappingResolve, token)
			}
			idx, err := strconv.Atoi(token)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("%w: invalid array index %q at pointer %q", ErrMappingResolve, token, ptr)
			}
			cur = v[idx]
		default:
			return nil, fmt.Errorf("%w: cannot descend into scalar at pointer %q", ErrMappingResolve, ptr)
		}
	}
	return cur, nil
}
