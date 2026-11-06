package contract

import (
	"encoding/json"
	"fmt"
	"time"
)

// SessionHandle is opaque provider state, never a transcript or a portable
// conversation. RunnerVersion identifies the actual provider runner. Adapters
// must verify it and the provider state before resuming, and fail if either is
// missing or incompatible. Recoverable permits reuse after worker restart on
// the same environment; it never permits migration to another worker.
type SessionHandle struct {
	ID               string    `json:"id"`
	Executor         string    `json:"executor"`
	RunnerVersion    string    `json:"runnerVersion"`
	OwnerWorker      string    `json:"ownerWorker"`
	OwnerEnvironment string    `json:"ownerEnvironment"`
	ExpiresAt        time.Time `json:"expiresAt"`
	Recoverable      bool      `json:"recoverable"`
}

// SessionVersion requires the structured session protocol, including strict
// resume and runner-version verification, rather than legacy bare IDs.
const SessionVersion = "2"

type SessionRequest struct {
	Mode   string         `json:"mode"` // create or resume; omission means one-shot
	Handle *SessionHandle `json:"handle,omitempty"`
}

// Equal compares handle identity across JSON round trips. Time zone and
// monotonic-clock representation are not part of an expiration instant.
func (s SessionHandle) Equal(other SessionHandle) bool {
	return s.ID == other.ID && s.Executor == other.Executor &&
		s.RunnerVersion == other.RunnerVersion && s.OwnerWorker == other.OwnerWorker &&
		s.OwnerEnvironment == other.OwnerEnvironment && s.ExpiresAt.Equal(other.ExpiresAt) &&
		s.Recoverable == other.Recoverable
}

func (s *SessionHandle) Validate() error {
	if s.ID == "" || s.Executor == "" || s.RunnerVersion == "" || s.ExpiresAt.IsZero() {
		return fmt.Errorf("session requires provider handle, executor, runner version and expiration")
	}
	return nil
}

func (r *Request) CheckSession() error {
	if r.Session == nil {
		return nil
	}
	var legacy struct {
		SessionID string `json:"sessionId"`
	}
	if len(r.Params) > 0 {
		_ = json.Unmarshal(r.Params, &legacy)
	}
	if r.SessionID != "" || legacy.SessionID != "" {
		return fmt.Errorf("session cannot be combined with legacy sessionId")
	}
	s := r.Session
	switch s.Mode {
	case "create":
		if s.Handle != nil {
			return fmt.Errorf("new session cannot supply an existing handle")
		}
	case "resume":
		if s.Handle == nil {
			return fmt.Errorf("resume requires a session handle; refusing to start a new conversation")
		}
		if err := s.Handle.Validate(); err != nil {
			return err
		}
		if s.Handle.OwnerWorker == "" || s.Handle.OwnerEnvironment == "" {
			return fmt.Errorf("resume requires session ownership")
		}
		if s.Handle.Executor != r.Executor {
			return fmt.Errorf("session executor mismatch")
		}
		if !time.Now().Before(s.Handle.ExpiresAt) {
			return fmt.Errorf("session expired; refusing to start a new conversation")
		}
	default:
		return fmt.Errorf("session mode must be create or resume")
	}
	return nil
}
