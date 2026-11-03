package state

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

var _ Store = (*Memory)(nil)

// Memory is the in-memory Store used when no data directory is configured.
type Memory struct {
	workflowVersions map[string][]byte
	mu               sync.RWMutex
	runs             map[string]*Run
	activations      map[string]*Activation
	events           map[string][]Event
	eventSeq         map[string]int
	timers           []*Timer
	signals          []*Signal
	signalSeq        map[string]int
	logChunks        []LogChunk
	assignments      map[string]*Assignment
	attempts         []ExecutionAttempt
	outbox           []*OutboxRecord
}

// New returns an in-memory store.
func New() *Memory {
	return &Memory{
		workflowVersions: make(map[string][]byte),
		runs:             make(map[string]*Run),
		activations:      make(map[string]*Activation),
		events:           make(map[string][]Event),
		eventSeq:         make(map[string]int),
		signalSeq:        make(map[string]int),
		assignments:      make(map[string]*Assignment),
	}
}

func (s *Memory) Close() error { return nil }

func (s *Memory) CreateRun(run *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.runs[run.ID]; ok {
		return fmt.Errorf("muxlio: run %q already exists", run.ID)
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	run.UpdatedAt = run.CreatedAt
	run.State = "created"
	s.runs[run.ID] = run
	s.eventSeq[run.ID] = 0
	s.appendEventLocked(run.ID, EventRunCreated, run.ID, nil)
	return nil
}

func (s *Memory) GetRun(id string) (*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, fmt.Errorf("muxlio: run %q not found", id)
	}
	return r, nil
}

func (s *Memory) SetRunState(id, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return fmt.Errorf("muxlio: run %q not found", id)
	}
	r.State = state
	r.UpdatedAt = time.Now()
	if eventType := EventTypeForRunState(state); eventType != "" {
		s.appendEventLocked(id, eventType, id, nil)
	}
	return nil
}

func (s *Memory) TransitionRunState(id string, expected []string, next string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return false, fmt.Errorf("muxlio: run %q not found", id)
	}
	allowed := false
	for _, state := range expected {
		if run.State == state {
			allowed = true
			break
		}
	}
	if !allowed {
		return false, nil
	}
	run.State = next
	run.UpdatedAt = time.Now()
	if eventType := EventTypeForRunState(next); eventType != "" {
		s.appendEventLocked(id, eventType, id, nil)
	}
	return true, nil
}

// RetryRun atomically requeues incomplete activations and restarts a terminal run.
func (s *Memory) RetryRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return fmt.Errorf("muxlio: run %q not found", id)
	}
	if run.State != "failed" && run.State != "cancelled" {
		return fmt.Errorf("muxlio: run %q cannot be retried from state %q", id, run.State)
	}
	for _, act := range s.activations {
		if act.RunID == id && act.State != "succeeded" {
			act.State = "queued"
			act.Error = ""
			s.appendEventLocked(id, EventActivationQueued, act.ID, map[string]any{"nodeId": act.NodeID, "scopePath": act.ScopePath})
		}
	}
	run.State = "running"
	run.Error = ""
	run.UpdatedAt = time.Now()
	s.appendEventLocked(id, EventRunRunning, id, nil)
	return nil
}

func (s *Memory) SetRunOutput(id string, output any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[id]; ok {
		r.Output = output
		r.UpdatedAt = time.Now()
		return nil
	}
	return fmt.Errorf("muxlio: run %q not found", id)
}

func (s *Memory) UpdateRunPlan(id string, planJSON json.RawMessage, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return fmt.Errorf("muxlio: run %q not found", id)
	}
	r.PlanJSON = append(json.RawMessage(nil), planJSON...)
	r.PlanDigest = digest
	r.UpdatedAt = time.Now()
	return nil
}

func (s *Memory) SetRunError(id string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[id]; ok {
		r.Error = err.Error()
		r.State = "failed"
		r.UpdatedAt = time.Now()
		s.appendEventLocked(id, EventRunFailed, id, map[string]any{"error": err.Error()})
		return nil
	}
	return fmt.Errorf("muxlio: run %q not found", id)
}

func (s *Memory) ListRuns() ([]*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	runs := make([]*Run, 0, len(s.runs))
	for _, r := range s.runs {
		runs = append(runs, r)
	}
	return runs, nil
}

func (s *Memory) CreateActivation(a *Activation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activations[a.ID]; ok {
		return fmt.Errorf("muxlio: activation %q already exists", a.ID)
	}
	s.activations[a.ID] = a
	s.appendEventLocked(a.RunID, EventActivationQueued, a.ID, map[string]any{
		"nodeId":    a.NodeID,
		"scopePath": a.ScopePath,
	})
	return nil
}

func (s *Memory) GetActivation(id string) (*Activation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.activations[id]
	if !ok {
		return nil, fmt.Errorf("muxlio: activation %q not found", id)
	}
	return a, nil
}

func (s *Memory) ListActivations(runID string) ([]Activation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Activation, 0)
	for _, a := range s.activations {
		if a.RunID == runID {
			copy := *a
			copy.ScopePath = append([]string(nil), a.ScopePath...)
			result = append(result, copy)
		}
	}
	return result, nil
}

func (s *Memory) SetActivationState(id, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.activations[id]
	if !ok {
		return fmt.Errorf("muxlio: activation %q not found", id)
	}
	a.State = state
	if eventType := EventTypeForActivationState(state); eventType != "" {
		s.appendEventLocked(a.RunID, eventType, a.ID, nil)
	}
	return nil
}

func (s *Memory) SetActivationOutputs(id string, outputs map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.activations[id]
	if !ok {
		return fmt.Errorf("muxlio: activation %q not found", id)
	}
	a.Outputs = outputs
	a.State = "succeeded"
	s.appendEventLocked(a.RunID, EventActivationSucceeded, id, map[string]any{
		"ports": outputKeys(outputs),
	})
	return nil
}

func (s *Memory) SetActivationError(id string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.activations[id]
	if !ok {
		return fmt.Errorf("muxlio: activation %q not found", id)
	}
	a.Error = err.Error()
	a.State = "failed"
	s.appendEventLocked(a.RunID, EventActivationFailed, id, map[string]any{"error": err.Error()})
	return nil
}

func (s *Memory) GetEvents(runID string, after int, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := s.events[runID]
	result := make([]Event, 0, len(events))
	for _, e := range events {
		if e.Seq <= after {
			continue
		}
		result = append(result, e)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (s *Memory) AppendEvent(runID, eventType, subjectID string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendEventLocked(runID, eventType, subjectID, payload)
	return nil
}

func (s *Memory) appendEventLocked(runID, eventType, subjectID string, payload map[string]any) {
	seq := s.eventSeq[runID] + 1
	s.eventSeq[runID] = seq
	s.events[runID] = append(s.events[runID], Event{
		RunID:     runID,
		Seq:       seq,
		Timestamp: time.Now(),
		Type:      eventType,
		SubjectID: subjectID,
		Payload:   payload,
	})
}

// --- timers ---

func (s *Memory) ScheduleTimer(id, runID, activationID string, due time.Time, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := marshalPayload(payload)
	s.timers = append(s.timers, &Timer{
		ID:           id,
		RunID:        runID,
		ActivationID: activationID,
		DueAt:        due,
		State:        "pending",
		Payload:      raw,
	})
	return nil
}

func (s *Memory) DueTimers(now time.Time) ([]Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []Timer
	for _, t := range s.timers {
		if t.State == "pending" && !t.DueAt.After(now) {
			t.State = "fired"
			due = append(due, *t)
		}
	}
	return due, nil
}

func (s *Memory) CancelTimer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.timers {
		if t.ID == id {
			t.State = "cancelled"
			return nil
		}
	}
	return nil
}

// --- signals ---

func (s *Memory) SendSignal(runID, name string, payload any, idempotencyKey ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check for duplicate idempotency key before appending.
	ikey := ""
	if len(idempotencyKey) > 0 {
		ikey = idempotencyKey[0]
	}
	if ikey != "" {
		for _, sig := range s.signals {
			if sig.RunID == runID && sig.Name == name && sig.IdempotencyKey == ikey {
				return nil // idempotent duplicate, silently drop
			}
		}
	}
	raw, _ := marshalPayload(payload)
	key := runID + ":" + name
	seq := s.signalSeq[key] + 1
	s.signalSeq[key] = seq
	s.signals = append(s.signals, &Signal{
		RunID:          runID,
		Name:           name,
		Seq:            seq,
		Payload:        raw,
		Timestamp:      time.Now(),
		IdempotencyKey: ikey,
	})
	return nil
}

func (s *Memory) ConsumeSignals(runID, name string) ([]Signal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var consumed []Signal
	for _, sig := range s.signals {
		if sig.RunID == runID && sig.Name == name {
			consumed = append(consumed, *sig)
		}
	}
	return consumed, nil
}

// --- chunked logs ---

func (s *Memory) AppendLogChunk(c LogChunk) error {
	if len(c.Data) > 64*1024 {
		return fmt.Errorf("muxlio: log chunk exceeds 64 KiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logChunks = append(s.logChunks, c)
	return nil
}

func (s *Memory) GetLogChunks(runID, activationID string) ([]LogChunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []LogChunk
	for _, c := range s.logChunks {
		if c.RunID == runID && (activationID == "" || c.ActivationID == activationID) {
			result = append(result, c)
		}
	}
	return result, nil
}

func (s *Memory) ListAssignments(runID string) ([]Assignment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Assignment, 0)
	for _, a := range s.assignments {
		if a.RunID == runID && a.State == "active" {
			result = append(result, *a)
		}
	}
	return result, nil
}

func (s *Memory) CancelRunWork(runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, assignment := range s.assignments {
		if assignment.RunID == runID && assignment.State == "active" {
			assignment.State = "cancelled"
			assignment.Generation++
		}
	}
	return nil
}

func (s *Memory) RecordExecutionAttempt(a ExecutionAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i := range s.attempts {
		if s.attempts[i].ID == a.ID {
			s.attempts[i] = a
			found = true
			break
		}
	}
	if !found {
		s.attempts = append(s.attempts, a)
	}
	payload := map[string]any{"attemptId": a.ID, "retryNumber": a.RetryNumber, "executor": a.Executor, "state": a.State, "startedAt": a.StartedAt, "finishedAt": a.FinishedAt, "failure": a.Failure}
	if a.Usage != nil {
		payload["usage"] = a.Usage
	}
	s.appendEventLocked(a.RunID, EventExecutionAttempt, a.ActivationID, payload)
	return nil
}

func (s *Memory) ListExecutionAttempts(runID, activationID string) ([]ExecutionAttempt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []ExecutionAttempt
	for _, a := range s.attempts {
		if a.RunID == runID && (activationID == "" || a.ActivationID == activationID) {
			result = append(result, a)
		}
	}
	return result, nil
}

// --- assignments ---

func (s *Memory) ClaimAssignment(a Assignment) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var generation int64
	if existing, ok := s.assignments[a.ActivationID]; ok {
		generation = existing.Generation + 1
	} else {
		generation = 1
	}
	a.Generation = generation
	a.State = "active"
	s.assignments[a.ActivationID] = &a
	return generation, nil
}

func (s *Memory) CompleteAssignment(activationID string, generation int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.assignments[activationID]
	if !ok || a.Generation != generation || a.State != "active" {
		return false, nil
	}
	a.State = "completed"
	return true, nil
}

// --- outbox ---

func (s *Memory) EnqueueOutbox(id, kind string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.outbox {
		if rec.ID == id {
			return fmt.Errorf("muxlio: outbox record %q already exists", id)
		}
	}
	s.outbox = append(s.outbox, &OutboxRecord{
		ID:        id,
		Kind:      kind,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
	return nil
}

func (s *Memory) ListUnpublishedOutbox(limit int) ([]OutboxRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var res []OutboxRecord
	for _, rec := range s.outbox {
		if rec.PublishedAt == nil {
			res = append(res, *rec)
			if limit > 0 && len(res) >= limit {
				break
			}
		}
	}
	return res, nil
}

func (s *Memory) MarkOutboxPublished(id string, publishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.outbox {
		if rec.ID == id {
			t := publishedAt
			rec.PublishedAt = &t
			return nil
		}
	}
	return fmt.Errorf("muxlio: outbox record %q not found", id)
}

func marshalPayload(v any) (map[string]any, error) {
	if v == nil {
		return nil, nil
	}
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	return nil, fmt.Errorf("muxlio: expected map[string]any payload, got %T", v)
}

func (s *Memory) SaveWorkflowVersion(digest string, planJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(planJSON))
	copy(cp, planJSON)
	s.workflowVersions[digest] = cp
	return nil
}

func (s *Memory) GetWorkflowVersion(digest string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.workflowVersions[digest]
	if !ok {
		return nil, fmt.Errorf("workflow version %q not found", digest)
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return cp, nil
}

func (s *Memory) ListWorkflowVersions() ([]WorkflowVersionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	summaries := make([]WorkflowVersionSummary, 0, len(s.workflowVersions))
	for digest, raw := range s.workflowVersions {
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &p)
		summaries = append(summaries, WorkflowVersionSummary{
			Digest:    digest,
			Name:      p.Name,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
	return summaries, nil
}
