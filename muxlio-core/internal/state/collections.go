package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrCollectionClosed   = errors.New("muxlio: collection is closed")
	ErrCollectionConflict = errors.New("muxlio: collection item ID already has a different payload")
	ErrCollectionCapacity = errors.New("muxlio: collection is at capacity")
)

// Leave room for collection metadata and the receipt in the MCP response.
const maxCollectionItemsBytes = (4 << 20) - 1024

type CollectionItem struct {
	ID    string          `json:"id"`
	Value json.RawMessage `json:"value"`
}

func CheckCollectionSize(items []CollectionItem) error {
	encoded, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if len(encoded) > maxCollectionItemsBytes {
		return ErrCollectionCapacity
	}
	return nil
}

type RunCollection struct {
	Name         string           `json:"name"`
	Items        []CollectionItem `json:"items"`
	ClosedReason string           `json:"closedReason,omitempty"`
	IdleDeadline *time.Time       `json:"idleDeadline,omitempty"`
}

type CollectionSpec struct {
	Seed        RunCollection
	MaxItems    int
	IdleTimeout time.Duration
	RunDeadline time.Time
}

// An empty action inspects the collection. Drained is the number of items
// completed by its consumer, which can start an idle interval.
type CollectionAction struct {
	Append      *CollectionItem
	InvalidItem error
	Close       bool
	Drained     *int
}

// Apply runs inside the same storage critical section as run state transitions.
// The caller persists changes even when the requested action was rejected.
func (a CollectionAction) Apply(c *RunCollection, spec CollectionSpec, runState string, now time.Time) (changed, accepted bool, err error) {
	active := runState == "created" || runState == "running" || runState == "waiting" || runState == "paused"
	if active && c.ClosedReason == "" && c.IdleDeadline != nil && !now.Before(*c.IdleDeadline) {
		c.ClosedReason = "idle_timeout"
		c.IdleDeadline = nil
		changed = true
	}
	if a.Append != nil {
		for _, item := range c.Items {
			if item.ID == a.Append.ID {
				if !bytes.Equal(item.Value, a.Append.Value) {
					return changed, false, ErrCollectionConflict
				}
				return changed, false, nil
			}
		}
	}
	if !active || (!spec.RunDeadline.IsZero() && !now.Before(spec.RunDeadline)) {
		if a.Append != nil || (a.Close && c.ClosedReason == "") {
			return changed, false, ErrCollectionClosed
		}
		return changed, false, nil
	}
	if c.ClosedReason != "" {
		if a.Append != nil {
			return changed, false, ErrCollectionClosed
		}
		return changed, false, nil
	}
	switch {
	case a.Append != nil:
		if a.InvalidItem != nil {
			return changed, false, a.InvalidItem
		}
		if len(c.Items) >= spec.MaxItems {
			return changed, false, ErrCollectionCapacity
		}
		items := append(c.Items, *a.Append)
		if err := CheckCollectionSize(items); err != nil {
			return changed, false, err
		}
		c.Items = items
		c.IdleDeadline = nil
		return true, true, nil
	case a.Close:
		c.ClosedReason = "explicit"
		c.IdleDeadline = nil
		return true, false, nil
	case a.Drained != nil && *a.Drained == len(c.Items) && c.IdleDeadline == nil:
		deadline := now.Add(spec.IdleTimeout).UTC()
		c.IdleDeadline = &deadline
		return true, false, nil
	}
	return changed, false, nil
}

// View includes terminal run state and the overall run deadline. Retrying a
// run can resume its collection unless the collection itself was closed.
func (c RunCollection) View(spec CollectionSpec, runState string, now time.Time) RunCollection {
	if c.ClosedReason == "" {
		switch runState {
		case "succeeded", "failed", "cancelled":
			c.ClosedReason = runState
		default:
			if !spec.RunDeadline.IsZero() && !now.Before(spec.RunDeadline) {
				c.ClosedReason = "run_deadline"
			}
		}
	}
	if c.ClosedReason != "" {
		c.IdleDeadline = nil
	}
	return c
}

func (s *Memory) UpdateRunCollection(runID string, spec CollectionSpec, action CollectionAction) (RunCollection, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	if run == nil {
		return RunCollection{}, false, errors.New("muxlio: run not found")
	}
	key := [2]string{runID, spec.Seed.Name}
	collection, exists := s.collections[key]
	if !exists {
		collection = spec.Seed
	}
	now := time.Now()
	changed, accepted, err := action.Apply(&collection, spec, run.State, now)
	if changed || !exists {
		s.collections[key] = cloneRunCollection(collection)
	}
	return cloneRunCollection(collection.View(spec, run.State, now)), accepted, err
}

func cloneRunCollection(c RunCollection) RunCollection {
	items := make([]CollectionItem, len(c.Items))
	for i, item := range c.Items {
		items[i] = CollectionItem{ID: item.ID, Value: bytes.Clone(item.Value)}
	}
	c.Items = items
	if c.IdleDeadline != nil {
		deadline := *c.IdleDeadline
		c.IdleDeadline = &deadline
	}
	return c
}
