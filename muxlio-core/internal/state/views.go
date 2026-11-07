package state

import (
	"fmt"
	"sort"
	"strings"
)

// OutputEntry lists a port without loading its contents. Open its activation to
// read the value; lists never embed inline artifact bytes or structured results.
type OutputEntry struct {
	RunID        string `json:"runId"`
	Digest       string `json:"digest,omitempty"`
	Mime         string `json:"mime,omitempty"`
	Path         string `json:"path,omitempty"`
	Size         int64  `json:"size,omitempty"`
	ActivationID string `json:"activationId"`
	NodeID       string `json:"nodeId"`
	Name         string `json:"name"`
}

// RunViewStore supports bounded reads without decoding the entire run history.
// Existing execution methods retain their full-history semantics.
type RunViewStore interface {
	EventsBefore(string, int, int) ([]Event, error)
	RecentEvents(string, int) ([]Event, error)
	GetRunHeader(string) (*Run, error)
	ActiveActivationOffset(string, int) (int, error)
	GetAttempt(string, string) (*ExecutionAttempt, error)
	LatestEventSeq(string) (int, error)
	ListActivationPage(string, int, int, bool) ([]Activation, error)
	ListExecutionAttemptPage(string, string, int, int, bool) ([]ExecutionAttempt, error)
	GetLogChunkPage(string, string, int, int) ([]LogChunk, error)
	ListOutputPage(string, int, int, string) ([]OutputEntry, error)
	ActivationCounts(string) (int, int, error)
	SnapshotPage(string, string, string, int, int, bool) ([]Event, error)
}

func pageSlice[T any](items []T, offset, limit int) []T {
	if offset > len(items) {
		offset = len(items)
	}
	end := len(items)
	if limit >= 0 && offset+limit < end {
		end = offset + limit
	}
	return items[offset:end]
}
func (s *Memory) ListActivationPage(id string, offset, limit int, details bool) ([]Activation, error) {
	items, err := s.ListActivations(id)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	items = pageSlice(items, offset, limit)
	if !details {
		for i := range items {
			items[i].Outputs = nil
			items[i].Inputs = nil
		}
	}
	return items, err
}
func (s *Memory) ListExecutionAttemptPage(id, act string, offset, limit int, details bool) ([]ExecutionAttempt, error) {
	items, err := s.ListExecutionAttempts(id, act)
	items = pageSlice(items, offset, limit)
	if !details {
		for i := range items {
			if limit < 0 {
				items[i].Result = nil
				items[i].OperationRef = nil
				items[i].Failure = ""
				continue
			}
			if items[i].Result != nil {
				r := *items[i].Result
				r.Outputs = nil
				if r.ValidationFailure != nil {
					v := *r.ValidationFailure
					v.Actual = nil
					v.Expected = nil
					r.ValidationFailure = &v
				}
				items[i].Result = &r
			}
		}
	}
	return items, err
}
func (s *Memory) GetLogChunkPage(id, act string, offset, limit int) ([]LogChunk, error) {
	items, err := s.GetLogChunks(id, act)
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.ActivationID != b.ActivationID {
			return a.ActivationID < b.ActivationID
		}
		if a.Stream != b.Stream {
			return a.Stream < b.Stream
		}
		return a.Seq < b.Seq
	})
	return pageSlice(items, offset, limit), err
}
func (s *Memory) ListOutputPage(id string, offset, limit int, search string) ([]OutputEntry, error) {
	var acts []Activation
	if id != "" {
		var err error
		acts, err = s.ListActivations(id)
		if err != nil {
			return nil, err
		}
	} else {
		runs, err := s.ListRuns()
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			items, err := s.ListActivations(r.ID)
			if err != nil {
				return nil, err
			}
			acts = append(acts, items...)
		}
	}
	completed := map[string]int{}
	loaded := map[string]bool{}
	for _, a := range acts {
		if loaded[a.RunID] {
			continue
		}
		events, err := s.GetEvents(a.RunID, 0, 0)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.Type == EventActivationSucceeded {
				completed[e.RunID+"/"+e.SubjectID] = e.Seq
			}
		}
		loaded[a.RunID] = true
	}
	sort.Slice(acts, func(i, j int) bool {
		a, b := acts[i], acts[j]
		if a.RunID != b.RunID {
			return a.RunID > b.RunID
		}
		x, y := completed[a.RunID+"/"+a.ID], completed[b.RunID+"/"+b.ID]
		if x != y {
			return x > y
		}
		return a.ID > b.ID
	})
	items := []OutputEntry{}
	for _, a := range acts {
		keys := []string{}
		for key := range a.Outputs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			e := OutputEntry{RunID: a.RunID, ActivationID: a.ID, NodeID: a.NodeID, Name: key}
			if value, ok := a.Outputs[key].(map[string]any); ok {
				e.Digest, _ = value["digest"].(string)
				e.Mime, _ = value["mime"].(string)
				e.Path, _ = value["path"].(string)
				if size, ok := value["size"].(float64); ok {
					e.Size = int64(size)
				}
			}
			if id == "" && !strings.HasPrefix(e.Digest, "sha256:") {
				continue
			}
			if !strings.Contains(strings.ToLower(e.RunID+" "+e.NodeID+" "+e.Name+" "+e.Digest+" "+e.Path+" "+e.Mime), strings.ToLower(search)) {
				continue
			}
			items = append(items, e)
		}
	}
	return pageSlice(items, offset, limit), nil
}
func (s *Memory) ActivationCounts(id string) (int, int, error) {
	acts, err := s.ListActivations(id)
	done := 0
	for _, a := range acts {
		if a.State == "succeeded" {
			done++
		}
	}
	return len(acts), done, err
}
func (s *Memory) SnapshotPage(id, act, attempt string, after, limit int, current bool) ([]Event, error) {
	events, err := ListSnapshots(s, id, act, attempt, current)
	items := []Event{}
	for _, e := range events {
		if e.Seq > after {
			items = append(items, e)
		}
	}
	return pageSlice(items, 0, limit), err
}

func (s *Memory) LatestEventSeq(id string) (int, error) {
	events, err := s.GetEvents(id, 0, 0)
	seq := 0
	if len(events) > 0 {
		seq = events[len(events)-1].Seq
	}
	return seq, err
}

func (s *Memory) GetAttempt(runID, id string) (*ExecutionAttempt, error) {
	items, err := s.ListExecutionAttempts(runID, "")
	if err != nil {
		return nil, err
	}
	for _, a := range items {
		if a.ID == id {
			return &a, nil
		}
	}
	return nil, fmt.Errorf("attempt not found")
}

type RunIndexStore interface {
	ListRunPage(int, int, string, string, string) ([]*Run, int, map[string]int, error)
}

func (s *Memory) ListRunPage(offset, limit int, search, status, order string) ([]*Run, int, map[string]int, error) {
	runs, err := s.ListRuns()
	counts := map[string]int{}
	items := []*Run{}
	for _, r := range runs {
		counts[r.State]++
		if status != "" && r.State != status {
			continue
		}
		if !strings.Contains(strings.ToLower(r.ID+" "+r.WorkflowName), strings.ToLower(search)) {
			continue
		}
		copy := *r
		copy.Input = nil
		copy.Output = nil
		copy.PlanJSON = nil
		items = append(items, &copy)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if order == "workflow" {
			if a.WorkflowName == b.WorkflowName {
				return a.ID < b.ID
			}
			return a.WorkflowName < b.WorkflowName
		}
		left, right := a.UpdatedAt, b.UpdatedAt
		if order == "created" {
			left, right = a.CreatedAt, b.CreatedAt
		}
		if left.Equal(right) {
			return a.ID < b.ID
		}
		return left.After(right)
	})
	return pageSlice(items, offset, limit), len(items), counts, err
}

func (s *Memory) GetRunHeader(id string) (*Run, error) {
	r, err := s.GetRun(id)
	if err != nil {
		return nil, err
	}
	r.Input = nil
	r.Output = nil
	return r, nil
}
func (s *Memory) ActiveActivationOffset(id string, limit int) (int, error) {
	items, err := s.ListActivationPage(id, 0, -1, false)
	if err != nil {
		return 0, err
	}
	selected, best := 0, 10
	rank := func(state string) int {
		switch state {
		case "running":
			return 0
		case "waiting":
			return 1
		case "queued":
			return 2
		case "failed":
			return 3
		}
		return 4
	}
	for i, a := range items {
		r := rank(a.State)
		if r <= best {
			selected, best = i, r
		}
	}
	return selected / limit * limit, nil
}

func (s *Memory) RecentEvents(id string, limit int) ([]Event, error) {
	events, err := s.GetEvents(id, 0, 0)
	start := len(events) - limit
	if start < 0 {
		start = 0
	}
	return events[start:], err
}

// EventsBefore returns newest first; zero starts at the latest event.
func (s *Memory) EventsBefore(id string, before, limit int) ([]Event, error) {
	events, err := s.GetEvents(id, 0, 0)
	items := []Event{}
	for i := len(events) - 1; i >= 0 && len(items) < limit; i-- {
		if before == 0 || events[i].Seq < before {
			items = append(items, events[i])
		}
	}
	return items, err
}
