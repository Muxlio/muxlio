package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/contract"
	"sort"
	"sync"
	"time"
)

var _ Store = (*Memory)(nil)

// Memory is the in-memory Store used when no data directory is configured.
type Memory struct {
	sessionQueues    map[string]*SessionQueue
	workflowVersions map[string][]byte
	mu               sync.RWMutex
	runs             map[string]*Run
	activations      map[string]*Activation
	events           map[string][]Event
	eventSeq         map[string]int
	timers           []*Timer
	signals          []*Signal
	signalSeq        map[string]int
	inputRequests    map[string]*InputRequest // keyed by runID + "\x00" + requestID
	logChunks        []LogChunk
	assignments      map[string]*Assignment
	attempts         []ExecutionAttempt
	outbox           []*OutboxRecord
}

// New returns an in-memory store.
func New() *Memory {
	return &Memory{
		workflowVersions: make(map[string][]byte),
		sessionQueues:    make(map[string]*SessionQueue),
		runs:             make(map[string]*Run),
		activations:      make(map[string]*Activation),
		events:           make(map[string][]Event),
		eventSeq:         make(map[string]int),
		signalSeq:        make(map[string]int),
		inputRequests:    make(map[string]*InputRequest),
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
	snapshot, err := cloneRun(run)
	if err != nil {
		return err
	}
	s.runs[run.ID] = snapshot
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
	return cloneRun(r)
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
	for _, attempt := range s.attempts {
		if attempt.RunID == id && attempt.AwaitReconciliationReason != "" {
			return fmt.Errorf("attempt %s awaits reconciliation: %s", attempt.ID, attempt.AwaitReconciliationReason)
		}
	}
	incomplete := make(map[string]bool)
	for _, act := range s.activations {
		if act.RunID == id && act.State != "succeeded" {
			incomplete[act.ID] = true
		}
	}
	var requests []InputRequest
	for _, req := range s.inputRequests {
		if req.RunID == id {
			requests = append(requests, *req)
		}
	}
	for _, req := range RenewCancelledInputs(requests, incomplete, time.Now()) {
		s.inputRequests[inputRequestKey(id, req.RequestID)] = &req
	}
	for _, act := range s.activations {
		if act.RunID == id && act.State != "succeeded" {
			for _, attempt := range s.attempts {
				if attempt.ActivationID == act.ID && attempt.RetryNumber > act.AttemptBase {
					act.AttemptBase = attempt.RetryNumber
				}
			}
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
		if r.State == "cancelled" {
			return nil
		}
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
	if r.PlanDigest != digest || !bytes.Equal(r.PlanJSON, planJSON) {
		return fmt.Errorf("muxlio: run %q has an immutable plan; create a new run", id)
	}
	return nil
}

func (s *Memory) SetRunError(id string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[id]; ok {
		if r.State == "cancelled" || r.State == "succeeded" {
			return nil
		}
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
		snapshot, err := cloneRun(r)
		if err != nil {
			return nil, err
		}
		runs = append(runs, snapshot)
	}
	return runs, nil
}

func (s *Memory) CreateActivation(a *Activation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[a.RunID]; run != nil && run.State == "cancelled" {
		return context.Canceled
	}
	if _, ok := s.activations[a.ID]; ok {
		return fmt.Errorf("muxlio: activation %q already exists", a.ID)
	}
	snapshot, err := cloneActivation(a)
	if err != nil {
		return err
	}
	s.activations[a.ID] = snapshot
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
	return cloneActivation(a)
}

func (s *Memory) ListActivations(runID string) ([]Activation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Activation, 0)
	for _, a := range s.activations {
		if a.RunID == runID {
			copy, err := cloneActivation(a)
			if err != nil {
				return nil, err
			}
			result = append(result, *copy)
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
	if a.State == "cancelled" {
		return nil
	}

	a.State = state
	if eventType := EventTypeForActivationState(state); eventType != "" {
		s.appendEventLocked(a.RunID, eventType, a.ID, nil)
	}
	return nil
}

func (s *Memory) SetActivationOutputs(id string, outputs map[string]any) error {
	return s.setActivationOutputs(id, outputs, "succeeded")
}

func (s *Memory) CheckpointActivation(id string, outputs map[string]any) error {
	return s.setActivationOutputs(id, outputs, "waiting")
}

func (s *Memory) setActivationOutputs(id string, outputs map[string]any, next string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.activations[id]
	if !ok {
		return fmt.Errorf("muxlio: activation %q not found", id)
	}
	if a.State == "cancelled" || a.State == "succeeded" || a.State == "failed" {
		return nil
	}

	if run := s.runs[a.RunID]; run != nil && run.State == "cancelled" {
		return context.Canceled
	}
	snapshot, err := cloneOutputs(outputs)
	if err != nil {
		return err
	}
	a.Outputs = snapshot
	a.State = next
	s.appendEventLocked(a.RunID, EventTypeForActivationState(next), id, map[string]any{
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
	if a.State == "cancelled" || a.State == "succeeded" || a.State == "failed" {
		return nil
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
	for _, timer := range s.timers {
		if timer.ID == id {
			return nil
		}
	}
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

// GetTimer returns the original deadline, including after it has fired.
func (s *Memory) GetTimer(id string) (*Timer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, timer := range s.timers {
		if timer.ID == id {
			snapshot := *timer
			return &snapshot, nil
		}
	}
	return nil, fmt.Errorf("muxlio: timer %q not found", id)
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

// --- input requests ---

func inputRequestKey(runID, requestID string) string { return runID + "\x00" + requestID }

// CreateInputRequest is idempotent: recreating a request for an existing run
// and request ID is a no-op, so run retries never clobber an accepted response.
func (s *Memory) CreateInputRequest(req *InputRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := inputRequestKey(req.RunID, req.RequestID)
	if _, ok := s.inputRequests[key]; ok {
		return nil
	}
	if req.State == "" {
		req.State = InputRequestPending
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	clone := cloneInputRequest(req)
	// Cancellation and creation share the run-state lock. Keep a closed
	// identity for late creation so explicit retry can renew this wait.
	if run := s.runs[req.RunID]; run != nil && run.State == "cancelled" && clone.State == InputRequestPending {
		clone.State = InputRequestCancelled
	}
	s.inputRequests[key] = &clone
	return nil
}

func (s *Memory) GetInputRequest(runID, requestID string) (*InputRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	req, ok := s.inputRequests[inputRequestKey(runID, requestID)]
	if !ok {
		return nil, fmt.Errorf("muxlio: input request %q not found for run %q", requestID, runID)
	}
	clone := cloneInputRequest(req)
	return &clone, nil
}

func (s *Memory) ListInputRequests(runID string) ([]InputRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []InputRequest
	for _, req := range s.inputRequests {
		if req.RunID == runID {
			clone := cloneInputRequest(req)
			result = append(result, clone)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].RequestID < result[j].RequestID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

// SubmitInputResponse delivers a response at most once. The first submission
// with a given idempotency key fulfills the request; a second call with the
// same key returns the already-accepted request (accepted=false). A request
// already fulfilled with a different key, or expired/cancelled, is rejected.
func (s *Memory) SubmitInputResponse(runID, requestID string, response any, responderID, idempotencyKey string) (*InputRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.inputRequests[inputRequestKey(runID, requestID)]
	if !ok {
		return nil, false, fmt.Errorf("muxlio: input request %q not found for run %q", requestID, runID)
	}
	if req.State == InputRequestFulfilled {
		if idempotencyKey != "" && req.IdempotencyKey == idempotencyKey {
			clone := cloneInputRequest(req)
			return &clone, false, nil // idempotent replay
		}
		return nil, false, ErrInputRequestNotAccepting
	}
	// Preserve replay above, but fence first acceptance against durable
	// cancellation even before CancelRunWork has closed pending requests.
	if run := s.runs[runID]; run != nil && run.State == "cancelled" {
		return nil, false, ErrInputRequestNotAccepting
	}
	if req.State == InputRequestPending && req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		req.State = InputRequestExpired
	}
	if req.State != InputRequestPending {
		return nil, false, ErrInputRequestNotAccepting
	}
	now := time.Now()
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, false, err
	}
	req.State = InputRequestFulfilled
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&req.Response)
	req.ResponderID = responderID
	req.IdempotencyKey = idempotencyKey
	req.FulfilledAt = &now
	clone := cloneInputRequest(req)
	return &clone, true, nil
}

func cloneInputRequest(req *InputRequest) InputRequest {
	raw, _ := json.Marshal(req)
	var clone InputRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&clone)
	return clone
}

func (s *Memory) ExpireInputRequests(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for _, req := range s.inputRequests {
		if req.State == InputRequestPending && req.ExpiresAt != nil && !req.ExpiresAt.After(now) {
			req.State = InputRequestExpired
			n++
		}
	}
	return n, nil
}

// --- chunked logs ---

func (s *Memory) AppendLogChunk(c LogChunk) error {
	if len(c.Data) > 64*1024 {
		return fmt.Errorf("muxlio: log chunk exceeds 64 KiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kind, subject, payload := ProgressEvent(c)

	count, size := 0, 0
	for _, event := range s.events[c.RunID] {
		if event.Type == kind && event.SubjectID == subject {
			count++
			if text, ok := event.Payload["text"].(string); ok {
				size += len(text)
			}
			if snapshot, ok := event.Payload["snapshot"].(ArtifactSnapshot); ok {
				size += int(snapshot.Size)
			}
			if event.Payload["stream"] == c.Stream && event.Payload["sourceSeq"] == c.Seq && event.Payload["generation"] == payload["generation"] && event.Payload["coordinatorEpoch"] == payload["coordinatorEpoch"] {
				return nil
			}
		}
	}
	if c.Snapshot != nil {
		if count >= MaxAttemptSnapshots || c.Snapshot.Size < 0 || c.Snapshot.Size > 4<<20 || int64(size)+c.Snapshot.Size > MaxAttemptSnapshotBytes {
			return nil
		}
	} else if count >= MaxAttemptProgressEvents || size+len(c.Data) > MaxAttemptProgressBytes {
		return nil
	}
	if c.Stream == "operation" {
		ref, ok := payload["operationRef"].(contract.RemoteTaskReference)
		if !ok {
			return fmt.Errorf("invalid operation reference")
		}
		s.appendEventLocked(c.RunID, kind, subject, payload)
		for i := range s.attempts {
			if s.attempts[i].ID == c.AttemptID {
				s.attempts[i].OperationRef = &ref
			}
		}
		return nil
	}
	s.appendEventLocked(c.RunID, kind, subject, payload)
	if c.Snapshot != nil {
		return nil
	}
	c.Data = append([]byte(nil), c.Data...)
	c.Seq = 1
	for _, prior := range s.logChunks {
		if prior.ActivationID == c.ActivationID && prior.Stream == c.Stream && prior.Seq >= c.Seq {
			c.Seq = prior.Seq + 1
		}
	}
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
	for _, req := range s.inputRequests {
		if req.RunID == runID && req.State == InputRequestPending {
			req.State = InputRequestCancelled
		}
	}
	for _, assignment := range s.assignments {
		if assignment.RunID == runID && (assignment.State == "active" || assignment.State == "uncertain") {
			assignment.State = "cancelled"
			assignment.Generation++
		}
	}
	for _, act := range s.activations {
		if act.RunID == runID && act.State != "succeeded" && act.State != "failed" && act.State != "cancelled" {
			act.State = "cancelled"
			s.appendEventLocked(runID, EventActivationCancelled, act.ID, map[string]any{"termination": "unconfirmed", "externalEffects": "completion unknown"})
		}
	}
	for _, timer := range s.timers {
		if timer.RunID == runID && timer.State == "pending" {
			timer.State = "cancelled"
		}
	}
	return nil
}

func (s *Memory) RecordExecutionAttempt(a ExecutionAttempt) error {
	if a.OperationRef != nil {
		ref := *a.OperationRef
		a.OperationRef = &ref
	}
	if a.Accounting != nil {
		snapshot, err := AccountingForAttempt(a)
		if err != nil {
			return err
		}
		a.Accounting = snapshot
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i := range s.attempts {
		if s.attempts[i].ID == a.ID {
			if err := ValidateAttemptOwnership(s.attempts[i], a); err != nil {
				return err
			}
			if a.RunID == "" {
				a.RunID = s.attempts[i].RunID
			}
			if a.ActivationID == "" {
				a.ActivationID = s.attempts[i].ActivationID
			}
			if a.Executor == "" {
				a.Executor = s.attempts[i].Executor
			}
			a.AccountingRemoteRevision = s.attempts[i].AccountingRemoteRevision
			a.AccountingRemoteHash = s.attempts[i].AccountingRemoteHash
			if a.OperationAttemptID == "" {
				a.OperationAttemptID = s.attempts[i].OperationAttemptID
			}
			if a.OperationAction == "" {
				a.OperationAction = s.attempts[i].OperationAction
			}
			priorAccounting := s.attempts[i].Accounting
			if a.Accounting != nil && priorAccounting != nil {
				if err := a.Accounting.Validate(); err != nil {
					return err
				}
				if a.Accounting.Revision == priorAccounting.Revision {
					oldBytes, _ := contract.CanonicalAccounting(*priorAccounting)
					newBytes, _ := contract.CanonicalAccounting(*a.Accounting)
					if !bytes.Equal(oldBytes, newBytes) {
						return fmt.Errorf("accounting revision conflict")
					}
					a.Accounting = priorAccounting
				} else if priorAccounting.Final && a.Accounting.Revision > priorAccounting.Revision {
					return fmt.Errorf("accounting snapshot is already final")
				} else if a.Accounting.Revision < priorAccounting.Revision {
					a.Accounting = priorAccounting
				}
			}
			if s.attempts[i].IdempotencyKey != "" {
				a.IdempotencyKey = s.attempts[i].IdempotencyKey
			}
			if s.attempts[i].Effect != "" {
				a.Effect = s.attempts[i].Effect
			}
			if a.OperationRef == nil {
				a.OperationRef = s.attempts[i].OperationRef
			}
			if a.ExecutionDurationMS == nil {
				a.ExecutionDurationMS = s.attempts[i].ExecutionDurationMS
			}

			if a.Accounting == nil {
				a.Accounting = s.attempts[i].Accounting
			}
			if a.Result == nil {
				a.Result = s.attempts[i].Result
			}
			s.attempts[i] = a
			found = true
			break
		}
	}
	if !found {
		s.attempts = append(s.attempts, a)
	}
	payload := map[string]any{"attemptId": a.ID, "retryNumber": a.RetryNumber, "executor": a.Executor, "state": a.State, "startedAt": a.StartedAt, "finishedAt": a.FinishedAt, "failure": a.Failure}
	if a.Result != nil {
		payload["result"] = a.Result
	}

	if a.ExecutionDurationMS != nil {
		payload["executionDurationMs"] = *a.ExecutionDurationMS
	}
	payload["effect"] = a.Effect
	payload["idempotencyKey"] = a.IdempotencyKey
	payload["operationRef"] = a.OperationRef
	payload["awaitReconciliationReason"] = a.AwaitReconciliationReason
	s.appendEventLocked(a.RunID, EventExecutionAttempt, a.ActivationID, payload)
	return nil
}

func (s *Memory) RecordAccountingSnapshot(attemptID string, snapshot contract.Accounting) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.attempts {
		a := &s.attempts[i]
		if a.ID != attemptID {
			continue
		}
		if a.Accounting != nil {
			if snapshot.Revision < a.Accounting.Revision {
				return fmt.Errorf("stale accounting revision")
			}
			if snapshot.Revision == a.Accounting.Revision {
				old, _ := contract.CanonicalAccounting(*a.Accounting)
				next, _ := contract.CanonicalAccounting(snapshot)
				if !bytes.Equal(old, next) {
					return fmt.Errorf("accounting revision conflict")
				}
				return nil
			}
			if a.Accounting.Final {
				return fmt.Errorf("accounting snapshot is already final")
			}
		}
		raw, err := contract.CanonicalAccounting(snapshot)
		if err != nil {
			return err
		}
		var cloned contract.Accounting
		if err := json.Unmarshal(raw, &cloned); err != nil {
			return err
		}
		a.Accounting = &cloned
		s.appendEventLocked(a.RunID, EventAccountingUpdated, a.ActivationID, map[string]any{"attemptId": a.ID, "revision": snapshot.Revision, "hash": fmt.Sprintf("%x", sha256.Sum256(raw))})
		return nil
	}
	return fmt.Errorf("accounting attempt does not exist")
}

func (s *Memory) ListExecutionAttempts(runID, activationID string) ([]ExecutionAttempt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []ExecutionAttempt
	for _, a := range s.attempts {
		if a.RunID == runID && (activationID == "" || a.ActivationID == activationID) {
			if a.OperationRef != nil {
				ref := *a.OperationRef
				a.OperationRef = &ref
			}
			var err error
			a.Accounting, err = AccountingForAttempt(a)
			if err != nil {
				return nil, err
			}
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
	if existing, ok := s.workflowVersions[digest]; ok && !bytes.Equal(existing, planJSON) {
		return fmt.Errorf("muxlio: workflow version %q is immutable", digest)
	}
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

// cloneRun prevents callers from changing pinned bytes or input through aliases.
func cloneRun(run *Run) (*Run, error) {
	raw, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	var snapshot Run
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	snapshot.PlanJSON = append(json.RawMessage(nil), run.PlanJSON...)
	return &snapshot, nil
}

// cloneOutputs isolates successful checkpoints from executor and caller mutation.
func cloneOutputs(outputs map[string]any) (map[string]any, error) {
	if outputs == nil {
		return nil, nil
	}
	raw, err := json.Marshal(outputs)
	if err != nil {
		return nil, err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func cloneActivation(a *Activation) (*Activation, error) {
	snapshot := *a
	snapshot.ScopePath = append([]string(nil), a.ScopePath...)
	snapshot.Inputs = append(json.RawMessage(nil), a.Inputs...)
	outputs, err := cloneOutputs(a.Outputs)
	if err != nil {
		return nil, err
	}
	snapshot.Outputs = outputs
	return &snapshot, nil
}
