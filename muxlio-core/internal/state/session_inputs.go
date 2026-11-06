package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
)

var ErrSessionQueueNotFound = errors.New("session queue not found")

var ErrSessionInputConflict = errors.New("session input identity conflict")

// SessionQueue is versioned independently of the plan. Revision fences concurrent
// submissions and receipts; a request remains accepted until an adapter acks it.
type SessionQueue struct {
	Version     string                 `json:"version"`
	Revision    int64                  `json:"revision"`
	RunID       string                 `json:"runId"`
	Handle      contract.SessionHandle `json:"handle"`
	Boundary    bool                   `json:"boundary"`
	Live        bool                   `json:"live"`
	Permissions []string               `json:"permissions"`
	Inputs      []QueuedSessionInput   `json:"inputs"`
}
type QueuedSessionInput struct {
	contract.SessionInput
	CallerID      string     `json:"callerId"`
	Policy        string     `json:"policy"`
	Delivery      string     `json:"delivery"`
	Status        string     `json:"status"`
	AcceptedAt    time.Time  `json:"acceptedAt"`
	DeliveredAt   *time.Time `json:"deliveredAt,omitempty"`
	AttemptID     string     `json:"attemptId,omitempty"`
	LiveAttemptID string     `json:"liveAttemptId,omitempty"`
}
type SessionQueueStore interface {
	GetSessionQueue(runID, sessionID string) (*SessionQueue, error)
	CompareAndSwapSessionQueue(queue *SessionQueue, revision int64) (bool, error)
}

func cloneSessionQueue(q *SessionQueue) *SessionQueue {
	raw, _ := json.Marshal(q)
	var clone SessionQueue
	_ = json.Unmarshal(raw, &clone)
	return &clone
}

// UpdateSessionQueue retries only revision conflicts, never external effects.
func UpdateSessionQueue(s SessionQueueStore, runID, sessionID string, update func(*SessionQueue) error) (*SessionQueue, error) {
	for {
		q, err := s.GetSessionQueue(runID, sessionID)
		if err != nil {
			return nil, err
		}
		revision := q.Revision
		if err := update(q); err != nil {
			return nil, err
		}
		ok, err := s.CompareAndSwapSessionQueue(q, revision)
		if err != nil {
			return nil, err
		}
		if ok {
			q.Revision = revision + 1
			return q, nil
		}
	}
}

func AcceptSessionInput(s SessionQueueStore, runID, sessionID string, input QueuedSessionInput) (*SessionQueue, error) {
	return UpdateSessionQueue(s, runID, sessionID, func(q *SessionQueue) error {
		for _, old := range q.Inputs {
			if old.RequestID == input.RequestID {
				if old.Version != input.Version || old.Message != input.Message || old.CallerID != input.CallerID || old.Policy != input.Policy {
					return ErrSessionInputConflict
				}
				return nil
			}
		}
		if q.Version != contract.SessionInputVersion || input.Version != q.Version || input.RequestID == "" || input.Message == "" {
			return fmt.Errorf("invalid session input")
		}
		if !time.Now().Before(q.Handle.ExpiresAt) {
			return fmt.Errorf("session expired")
		}
		if input.Policy != "next-boundary" && input.Policy != "live-only" && input.Policy != "live-or-next-boundary" {
			return fmt.Errorf("invalid input delivery policy")
		}
		input.Delivery = "next-boundary"
		if input.Policy != "next-boundary" && q.Live {
			input.Delivery = "live"
		}
		if input.Policy == "live-only" && !q.Live {
			return fmt.Errorf("live input unsupported")
		}
		if input.Delivery == "next-boundary" && !q.Boundary {
			return fmt.Errorf("boundary input unsupported")
		}
		input.Status = "accepted"
		input.AcceptedAt = time.Now().UTC()
		q.Inputs = append(q.Inputs, input)
		return nil
	})
}

// BoundaryInputs snapshots pending fallback input for a durable attempt. Its
// dispatch envelope pins the batch; unacknowledged input remains eligible for a
// later turn. Adapters deduplicate live/boundary races and lost receipts.
func BoundaryInputs(s SessionQueueStore, runID, sessionID, attemptID string) ([]contract.SessionInput, error) {
	var inputs []contract.SessionInput
	_, err := UpdateSessionQueue(s, runID, sessionID, func(q *SessionQueue) error {
		inputs = nil
		for i := range q.Inputs {
			input := &q.Inputs[i]
			if !q.Boundary || input.Status != "accepted" || input.Policy == "live-only" {
				continue
			}
			input.AttemptID = attemptID
			input.Delivery = "next-boundary"
			inputs = append(inputs, input.SessionInput)
		}
		return nil
	})
	return inputs, err
}

// CheckSessionInputAcknowledgments validates receipts before a completion is
// persisted. Invalid receipts must not poison an immutable worker result.
func CheckSessionInputAcknowledgments(offered []contract.SessionInput, acknowledged []string) error {
	allowed := map[string]bool{}
	for _, input := range offered {
		allowed[input.RequestID] = true
	}
	for _, id := range acknowledged {
		if !allowed[id] {
			return fmt.Errorf("adapter acknowledged input that was not offered")
		}
	}
	return nil
}

func AcknowledgeSessionInputs(s SessionQueueStore, runID, sessionID string, offered []contract.SessionInput, acknowledged []string) error {
	if err := CheckSessionInputAcknowledgments(offered, acknowledged); err != nil {
		return err
	}
	_, err := UpdateSessionQueue(s, runID, sessionID, func(q *SessionQueue) error {
		for i := range q.Inputs {
			for _, id := range acknowledged {
				if q.Inputs[i].RequestID == id && q.Inputs[i].Status != "delivered" {
					now := time.Now().UTC()
					q.Inputs[i].Status, q.Inputs[i].DeliveredAt = "delivered", &now
				}
			}
		}
		return nil
	})
	return err
}

func (s *Memory) GetSessionQueue(runID, sessionID string) (*SessionQueue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := s.sessionQueues[inputRequestKey(runID, sessionID)]
	if q == nil {
		return nil, ErrSessionQueueNotFound
	}
	return cloneSessionQueue(q), nil
}
func (s *Memory) CompareAndSwapSessionQueue(q *SessionQueue, revision int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[q.RunID]; !ok {
		return false, fmt.Errorf("run not found")
	}
	key := inputRequestKey(q.RunID, q.Handle.ID)
	old := s.sessionQueues[key]
	if (old == nil && revision != 0) || (old != nil && old.Revision != revision) {
		return false, nil
	}
	clone := cloneSessionQueue(q)
	clone.Revision = revision + 1
	s.sessionQueues[key] = clone
	return true, nil
}
