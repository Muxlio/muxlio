// Package subscription defines optional, signed outbound lifecycle
// subscriptions: durable records that an external system wants to be told when
// a long workflow (or worker-driven activity within it) reaches a terminal or
// wait state, and an asynchronous dispatcher that delivers those records.
//
// A subscription owns the secret used to SIGN outbound deliveries (the outbound
// counterpart of package trigger, which verifies inbound signatures). The
// coordinator never learns the receiver's protocol: delivery is a generic
// HMAC-signed HTTP POST (or a local in-process hook) over a redacted envelope.
// Delivery is fully decoupled from workflow execution: the dispatcher runs in
// its own goroutine, so a subscriber outage can never block a run from
// completing. Each delivered event carries a stable, durable identifier derived
// from the run and its event sequence, so at-least-once redelivery is
// idempotent from the receiver's perspective. Delivery failures are bounded by
// retries and, after exhaustion, dead-lettered for inspection — they never
// change the run's outcome.
package subscription

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
	"net/url"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"github.com/muxlio/muxlio-core/internal/state"
)

// Configuration defaults and bounds. A subscription without explicit limits
// still enforces safe defaults so a misconfigured subscription cannot retry
// forever or carry an unbounded payload.
const (
	SchemeHMACSHA256          = "hmac-sha256"
	DefaultSignatureHeader    = "X-Muxlio-Signature"
	DefaultSignaturePrefix    = "sha256="
	EncodingHex               = "hex"
	EncodingBase64            = "base64"
	DefaultMaxRetries         = 5
	MinSecretBytes            = 16
	MaxIDLen                  = 128
	MaxTargetLen              = 2048
	MinRetries                = 0
	MaxRetries                = 50
	MaxRedactFields           = 256
	MaxEventTypes             = 64
	DeliveryPayloadLimitBytes = 1 << 20 // 1 MiB outbound envelope cap
)

// Delivery states.
const (
	StatePending    = "pending"
	StateDelivering = "delivering"
	StateDelivered  = "delivered"
	StateDeadLetter = "deadletter"
	StateRequeued   = "requeued"
)

// Sentinel errors. The API maps these to stable HTTP error codes.
var (
	ErrSubscriptionDisabled = errors.New("muxlio: subscription is disabled")
	ErrInvalidConfig        = errors.New("muxlio: subscription has an invalid configuration")
	ErrSecretMissing        = errors.New("muxlio: signing secret is missing or too short")
	ErrInvalidTarget        = errors.New("muxlio: target must be an http(s) URL or a local hook")
	ErrInvalidEventType     = errors.New("muxlio: event type filter is invalid")
	ErrRedactionConflict    = errors.New("muxlio: redaction field is reserved")
)

// Subscription is an immutable definition of an outbound lifecycle subscription.
// The Secret signs outbound deliveries and is the only field never serialized to
// API responses (json:"-"); the store retains it so the dispatcher can sign.
type Subscription struct {
	ID                string   `json:"id"`
	EventTypes        []string `json:"eventTypes"`
	Target            string   `json:"target"`
	Secret            string   `json:"-"`
	SignatureScheme   string   `json:"signatureScheme,omitempty"`
	SignatureHeader   string   `json:"signatureHeader,omitempty"`
	SignaturePrefix   string   `json:"signaturePrefix,omitempty"`
	SignatureEncoding string   `json:"signatureEncoding,omitempty"`
	RedactFields      []string `json:"redactFields,omitempty"`
	MaxRetries        int      `json:"maxRetries"`
	Enabled           bool     `json:"enabled"`
}

// Summary is a subscription without its secret, suitable for list/get responses.
// Because Secret carries json:"-", marshaling a Subscription already omits it;
// this alias documents the intent and keeps the surface stable.
type Summary = Subscription

// Delivery is one durable, at-least-once outbound delivery record. Its ID is
// deterministic — derived from the subscription and the source event — so
// retries and replays collapse to a single record and the receiver can dedup.
// MaxRetries is snapshotted from the subscription at enqueue time so a later
// config change cannot retroactively extend an already-exhausted delivery.
type Delivery struct {
	ID             string     `json:"id"`
	SubscriptionID string     `json:"subscriptionId"`
	RunID          string     `json:"runId"`
	EventSeq       int        `json:"eventSeq"`
	EventID        string     `json:"eventId"`
	EventType      string     `json:"eventType"`
	SubjectID      string     `json:"subjectId,omitempty"`
	Timestamp      time.Time  `json:"timestamp"`
	Payload        any        `json:"payload,omitempty"`
	State          string     `json:"state"`
	Attempts       int        `json:"attempts"`
	MaxRetries     int        `json:"maxRetries"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
}

// DeadLetter is a delivery that exhausted its retries. It is a Delivery in the
// deadletter state; this alias documents the inspection surface.
type DeadLetter = Delivery

// DeliveryResult records the outcome of one delivery attempt.
type DeliveryResult struct {
	Succeeded bool
	Error     string
}

// Envelope is the signed JSON body delivered to a receiver. Payload is redacted
// per the subscription before signing. EventID is stable across redeliveries.
type Envelope struct {
	EventID        string    `json:"eventId"`
	EventType      string    `json:"eventType"`
	RunID          string    `json:"runId"`
	Seq            int       `json:"seq"`
	Timestamp      time.Time `json:"timestamp"`
	SubscriptionID string    `json:"subscriptionId"`
	SubjectID      string    `json:"subjectId,omitempty"`
	Payload        any       `json:"payload"`
}

// Store persists subscriptions, their delivery records, and the fanout cursors
// that track which events the dispatcher has already enqueued deliveries for.
// SQLite implements this independently of the workflow state contract.
type Store interface {
	// Subscriptions.
	CreateSubscription(Subscription) error
	GetSubscription(id string) (Subscription, error)
	ListSubscriptions() ([]Subscription, error)
	DeleteSubscription(id string) error

	// Deliveries.
	// EnqueueDelivery idempotently creates a pending delivery; a record with
	// the same ID is a no-op so retried fanout cannot duplicate deliveries.
	EnqueueDelivery(Delivery) error
	GetDelivery(id string) (Delivery, error)
	// ListDueDeliveries returns deliveries that are pending or requeued and
	// whose NextAttemptAt is at or before now, excluding dead-lettered ones.
	ListDueDeliveries(now time.Time, limit int) ([]Delivery, error)
	// RecordDelivery applies the outcome of one attempt: success marks the
	// delivery delivered; failure increments attempts, schedules the next
	// retry at nextAttemptAt (when non-nil), or dead-letters once attempts
	// exceed the snapshotted MaxRetries (one initial attempt plus retries).
	RecordSubscriptionDelivery(id string, result DeliveryResult, nextAttemptAt *time.Time, now time.Time) error
	ListSubscriptionDeliveries(subscriptionID string, limit int) ([]Delivery, error)
	ListDeadLetters(limit int) ([]Delivery, error)
	// RequeueDeadLetter resets a dead-lettered delivery to pending so an
	// operator can retry after fixing the receiver.
	RequeueDeadLetter(id string, now time.Time) error

	// Fanout cursors track the last enqueued sequence per subscription/run key.
	GetCursor(runID string) (int, error)
	SetCursor(runID string, seq int) error
}

// EventReader is the minimum the dispatcher needs to discover new lifecycle
// events. state.Store satisfies it, keeping the dispatcher decoupled from any
// specific store implementation.
type EventReader interface {
	ListRuns() ([]*state.Run, error)
	GetEvents(runID string, after, limit int) ([]state.Event, error)
}

// Normalize applies safe defaults and validates the definition. It mutates the
// receiver so the caller can store the normalized form.
func (s *Subscription) Normalize() error {
	if s == nil {
		return errors.New("muxlio: subscription is nil")
	}
	if s.ID == "deadletters" || s.ID == "deliveries" {
		return errors.New("muxlio: reserved subscription id")
	}
	if s.ID == "" || len(s.ID) > MaxIDLen {
		return fmt.Errorf("muxlio: id must contain 1..%d characters", MaxIDLen)
	}
	for _, c := range s.ID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errors.New("muxlio: id must use letters, digits, - or _")
		}
	}
	if len(s.EventTypes) == 0 {
		return errors.New("muxlio: at least one event type filter is required")
	}
	if len(s.EventTypes) > MaxEventTypes {
		return fmt.Errorf("muxlio: eventTypes must contain at most %d entries", MaxEventTypes)
	}
	for _, et := range s.EventTypes {
		if !validEventTypeFilter(et) {
			return fmt.Errorf("%w: %q", ErrInvalidEventType, et)
		}
	}
	if err := normalizeTarget(s); err != nil {
		return err
	}
	if len([]byte(s.Secret)) < MinSecretBytes {
		return fmt.Errorf("muxlio: secret must be at least %d bytes", MinSecretBytes)
	}
	switch s.SignatureScheme {
	case "":
		s.SignatureScheme = SchemeHMACSHA256
	case SchemeHMACSHA256:
	default:
		return fmt.Errorf("muxlio: unsupported signature scheme %q", s.SignatureScheme)
	}
	if s.SignatureHeader == "" {
		s.SignatureHeader = DefaultSignatureHeader
	}
	if err := validHeaderName(s.SignatureHeader); err != nil {
		return err
	}
	switch s.SignatureEncoding {
	case "":
		s.SignatureEncoding = EncodingHex
	case EncodingHex, EncodingBase64:
	default:
		return fmt.Errorf("muxlio: unsupported signature encoding %q", s.SignatureEncoding)
	}
	if s.SignaturePrefix == "" {
		s.SignaturePrefix = DefaultSignaturePrefix
	}
	if strings.HasPrefix(s.SignaturePrefix, " ") {
		return errors.New("muxlio: signaturePrefix must not start with whitespace")
	}
	for _, c := range s.SignaturePrefix {
		if c < 0x20 || c > 0x7e {
			return errors.New("muxlio: signaturePrefix must contain printable ASCII characters")
		}
	}

	if s.MaxRetries < MinRetries || s.MaxRetries > MaxRetries {
		return fmt.Errorf("muxlio: maxRetries must be between %d and %d", MinRetries, MaxRetries)
	}
	if len(s.RedactFields) > MaxRedactFields {
		return fmt.Errorf("muxlio: redactFields must contain at most %d entries", MaxRedactFields)
	}
	for _, f := range s.RedactFields {
		if f == "" {
			return errors.New("muxlio: redactFields must not contain empty entries")
		}
		// eventId/runId/seq are part of the envelope contract; redacting them
		// would break deduplication and routing, so they are reserved.
		switch strings.ToLower(f) {
		case "eventid", "runid", "seq", "eventtype", "timestamp", "subscriptionid":
			return fmt.Errorf("%w: %q", ErrRedactionConflict, f)
		}
	}
	return nil
}

func normalizeTarget(s *Subscription) error {
	t := strings.TrimSpace(s.Target)
	s.Target = t
	if t == "" {
		return ErrInvalidTarget
	}
	if len(t) > MaxTargetLen {
		return fmt.Errorf("muxlio: target exceeds %d characters", MaxTargetLen)
	}
	if strings.HasPrefix(t, "local:") {
		name := strings.TrimPrefix(t, "local:")
		if name == "" || len(name) > MaxIDLen {
			return errors.New("muxlio: local hook name must contain 1..128 characters")
		}
		return nil
	}
	u, err := url.Parse(t)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Fragment == "" {
		return nil
	}
	return ErrInvalidTarget
}

// IsLocalHook reports whether the target names an in-process local hook.
func (s Subscription) IsLocalHook() bool {
	return strings.HasPrefix(s.Target, "local:")
}

// LocalHookName returns the hook name for a local target, or empty.
func (s Subscription) LocalHookName() string {
	if !s.IsLocalHook() {
		return ""
	}
	return strings.TrimPrefix(s.Target, "local:")
}

// Matches reports whether an event type is covered by this subscription's
// filters. A filter may be an exact event type, a "category.*" prefix, or "*"
// for every event.
func (s Subscription) Matches(eventType string) bool {
	for _, f := range s.EventTypes {
		if f == "*" {
			return true
		}
		if f == eventType {
			return true
		}
		if strings.HasSuffix(f, ".*") {
			prefix := strings.TrimSuffix(f, ".*")
			if eventType == prefix || strings.HasPrefix(eventType, prefix+".") {
				return true
			}
		}
	}
	return false
}

func validEventTypeFilter(et string) bool {
	if et == "*" {
		return true
	}
	if et == "" {
		return false
	}
	prefix := strings.HasSuffix(et, ".*")
	if prefix {
		et = strings.TrimSuffix(et, ".*")
	}
	if et == "" {
		return false
	}
	// Event types look like "category.name"; allow letters, digits, dot, - and _.
	for _, c := range et {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return strings.Contains(et, ".") || prefix
}

func validHeaderName(name string) error {
	// These fields are owned by the envelope or HTTP transport. Go may
	// overwrite, interpret, or omit them rather than send a signature.
	switch strings.ToLower(name) {
	case "content-type", "content-length", "host", "connection", "transfer-encoding", "trailer", "te", "upgrade", "keep-alive", "proxy-connection", "x-muxlio-event-id":
		return errors.New("muxlio: signatureHeader conflicts with a reserved HTTP header")
	}
	if name == "" {
		return errors.New("muxlio: signatureHeader must not be empty")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return errors.New("muxlio: signatureHeader must be a valid HTTP header name")
		}
	}
	return nil
}

// SignBody computes the signature header value for a raw delivery body using
// the subscription's secret and signature configuration. The returned value is
// prefixed with SignaturePrefix so receivers can strip a scheme prefix before
// decoding. Comparison against a receiver-computed value must be constant-time.
func (s Subscription) SignBody(body []byte) (string, error) {
	if !s.Enabled {
		return "", ErrSubscriptionDisabled
	}
	if len([]byte(s.Secret)) < MinSecretBytes || s.SignatureScheme != SchemeHMACSHA256 {
		return "", ErrInvalidConfig
	}
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write(body)
	sum := mac.Sum(nil)
	var encoded string
	switch s.SignatureEncoding {
	case "", EncodingHex:
		encoded = hex.EncodeToString(sum)
	case EncodingBase64:
		encoded = base64.StdEncoding.EncodeToString(sum)
	default:
		return "", ErrInvalidConfig
	}
	return s.SignaturePrefix + encoded, nil
}

// VerifyBody authenticates a raw delivery body against a supplied header value.
// Comparison is constant-time. A disabled subscription rejects every delivery.
func (s Subscription) VerifyBody(body []byte, headerValue string) error {
	if !s.Enabled {
		return ErrSubscriptionDisabled
	}
	want, err := s.SignBody(body)
	if err != nil {
		return err
	}
	// SignBody already applies the prefix; compare the full header value.
	if subtle.ConstantTimeCompare([]byte(headerValue), []byte(want)) != 1 {
		return errors.New("muxlio: signature does not match the delivery body")
	}
	return nil
}

// DeliveryID returns the stable, durable identifier for a delivery of event
// (runID, seq) to the given subscription. The same source event delivered to
// the same subscription always yields the same ID, so fanout retries and
// replays collapse to a single delivery record.
func DeliveryID(subscriptionID, runID string, seq int) string {
	return fmt.Sprintf("%s:%s:%d", subscriptionID, runID, seq)
}

// EventID returns the durable event identifier exposed to receivers. It is
// stable across redeliveries so a receiver can dedup at-least-once deliveries.
func EventID(runID string, seq int) string {
	return fmt.Sprintf("%s:%d", runID, seq)
}

// RedactPayload returns a copy of payload with configured and credential fields
// recursively replaced with "[REDACTED]" and string values passed through
// diagnostic.Redact so embedded credentials are stripped before delivery. The
// envelope contract fields (eventId, runId, seq, ...) are never redacted here;
// they are reserved by Normalize and excluded from RedactFields.
func RedactPayload(payload map[string]any, fields []string) map[string]any {
	if payload == nil {
		return nil
	}
	// Canonicalize typed maps, slices, structs and RawMessages before walking:
	// an extension's Go representation must never bypass field redaction.
	raw, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"data": "[REDACTED]"}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var normalized map[string]any
	if dec.Decode(&normalized) != nil {
		return map[string]any{"data": "[REDACTED]"}
	}
	redact := make(map[string]bool, len(fields))
	for _, f := range fields {
		redact[strings.ToLower(f)] = true
	}
	return redactValue(normalized, redact).(map[string]any)
}

func redactValue(v any, fields map[string]bool) any {
	switch x := v.(type) {
	case string:
		return diagnostic.Redact(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			lower := strings.ToLower(k)
			safeKey := diagnostic.Redact(k)
			if fields[lower] || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "authorization") || strings.Contains(lower, "apikey") || strings.Contains(lower, "api_key") || strings.Contains(lower, "api-key") {
				out[safeKey] = "[REDACTED]"
			} else {
				out[safeKey] = redactValue(v, fields)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = redactValue(v, fields)
		}
		return out
	default:
		return v
	}
}

// BuildEnvelope constructs the redacted, signed delivery body for a delivery
// under a subscription. It redacts the payload, builds the envelope, marshals
// it, and returns the body plus the signature header value. The body is capped
// at DeliveryPayloadLimitBytes; an oversize envelope is an error rather than a
// truncated delivery so receivers never see a partial signature.
func BuildEnvelope(sub Subscription, d Delivery) ([]byte, string, error) {
	var payload map[string]any
	if d.Payload != nil {
		if m, ok := d.Payload.(map[string]any); ok {
			payload = RedactPayload(m, sub.RedactFields)
		} else {
			// Non-object payloads are redacted as a whole string when possible.
			payload = RedactPayload(map[string]any{"data": d.Payload}, sub.RedactFields)
		}
	}
	env := Envelope{
		EventID:        d.EventID,
		EventType:      d.EventType,
		RunID:          d.RunID,
		Seq:            d.EventSeq,
		Timestamp:      d.Timestamp,
		SubscriptionID: sub.ID,
		SubjectID:      d.SubjectID,
		Payload:        payload,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return nil, "", err
	}
	if len(body) > DeliveryPayloadLimitBytes {
		return nil, "", fmt.Errorf("muxlio: delivery envelope exceeds %d bytes", DeliveryPayloadLimitBytes)
	}
	sig, err := sub.SignBody(body)
	if err != nil {
		return nil, "", err
	}
	return body, sig, nil
}
