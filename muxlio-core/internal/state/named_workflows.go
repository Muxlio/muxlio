package state

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	ErrNamedWorkflowNotFound       = errors.New("muxlio: named workflow not found")
	ErrNamedWorkflowConflict       = errors.New("muxlio: workflow current revision changed or ID already exists")
	ErrWorkflowVersionNotPublished = errors.New("muxlio: digest is not published in this workflow")
	ErrWorkflowVersionCurrent      = errors.New("muxlio: workflow version is current for a named workflow")
)

type NamedWorkflowVersion struct {
	Digest      string    `json:"digest"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"publishedAt"`
	Retired     bool      `json:"retired,omitempty"`
}

// NamedWorkflow resolves a stable ID to an immutable plan digest. The revision
// advances on each pointer change, including a switch back to an older digest.
type NamedWorkflow struct {
	ID              string                 `json:"workflowId"`
	CurrentDigest   string                 `json:"currentDigest"`
	CurrentRevision int64                  `json:"currentRevision"`
	Versions        []NamedWorkflowVersion `json:"versions"`
}

type WorkflowPublication struct {
	Digest string
	Name   string
	// A nil PlanJSON selects an already published version.
	PlanJSON []byte
}

// ApplyPublication updates a snapshot with newest-first publication history.
// It reports whether a version was added. Stores commit the snapshot change
// together with any new registry bytes while holding their write lock.
func (w *NamedWorkflow) ApplyPublication(expected int64, p WorkflowPublication) (bool, error) {
	if expected == 0 {
		if w.CurrentRevision != 0 {
			if p.Digest == w.Versions[len(w.Versions)-1].Digest && p.PlanJSON != nil {
				return false, nil
			}
			return false, ErrNamedWorkflowConflict
		}
	} else {
		if w.CurrentRevision == 0 {
			return false, ErrNamedWorkflowNotFound
		}
		if expected != w.CurrentRevision {
			if expected == w.CurrentRevision-1 && p.Digest == w.CurrentDigest {
				return false, nil
			}
			return false, fmt.Errorf("%w (current revision %d)", ErrNamedWorkflowConflict, w.CurrentRevision)
		}
		if p.Digest == w.CurrentDigest {
			return false, nil
		}
	}
	for _, v := range w.Versions {
		if v.Digest != p.Digest {
			continue
		}
		if v.Retired {
			return false, ErrWorkflowVersionRetired
		}
		if p.PlanJSON != nil {
			return false, nil
		}
		w.CurrentRevision++
		w.CurrentDigest = p.Digest
		return false, nil
	}
	if p.PlanJSON == nil {
		return false, ErrWorkflowVersionNotPublished
	}
	w.CurrentRevision++
	w.CurrentDigest = p.Digest
	w.Versions = slices.Insert(w.Versions, 0, NamedWorkflowVersion{
		Digest: p.Digest, Name: p.Name, PublishedAt: time.Now().UTC(),
	})
	return true, nil
}

// CurrentFirst returns a detached view with the current version at the front
// and the other versions in reverse publication order.
func (w NamedWorkflow) CurrentFirst() NamedWorkflow {
	w.Versions = slices.Clone(w.Versions)
	for i, v := range w.Versions {
		if v.Digest == w.CurrentDigest {
			copy(w.Versions[1:i+1], w.Versions[:i])
			w.Versions[0] = v
			break
		}
	}
	return w
}

func (s *Memory) PublishWorkflow(id string, expectedRevision int64, publication WorkflowPublication) (NamedWorkflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow := s.namedWorkflow(id)
	added, err := workflow.ApplyPublication(expectedRevision, publication)
	if err != nil {
		return NamedWorkflow{}, err
	}
	if added {
		if s.retiredWorkflowVersions[publication.Digest] {
			return NamedWorkflow{}, ErrWorkflowVersionRetired
		}
		if err := s.saveWorkflowVersion(publication.Digest, publication.PlanJSON); err != nil {
			return NamedWorkflow{}, err
		}
	}
	s.namedWorkflows[id] = workflow
	return workflow.CurrentFirst(), nil
}

func (s *Memory) GetNamedWorkflow(id string) (NamedWorkflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	workflow := s.namedWorkflow(id)
	if workflow.CurrentRevision == 0 {
		return NamedWorkflow{}, ErrNamedWorkflowNotFound
	}
	return workflow.CurrentFirst(), nil
}

func (s *Memory) namedWorkflow(id string) NamedWorkflow {
	workflow := s.namedWorkflows[id]
	workflow.ID = id
	workflow.Versions = slices.Clone(workflow.Versions)
	for i := range workflow.Versions {
		workflow.Versions[i].Retired = s.retiredWorkflowVersions[workflow.Versions[i].Digest]
	}
	return workflow
}
