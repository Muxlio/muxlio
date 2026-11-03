package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/coordinatorlog"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// WorkerHeartbeatWindow is how long after last_seen a worker is considered live.
const WorkerHeartbeatWindow = 90 * time.Second

// dispatchStore is implemented by durable stores that support worker polling.
type dispatchStore interface {
	RegisterWorker(state.Worker) error
	ListWorkers(time.Time) ([]state.Worker, error)
	EnqueueDispatch(state.Dispatch) error
	ClaimDispatch(string, time.Time) (*state.Dispatch, int64, error)
	CompleteWorkerAssignment(state.Assignment, string, time.Time, ...[]byte) (bool, error)
	ListAssignments(string) ([]state.Assignment, error)
	ReconcileUncertainDispatch(string) error
	StartCoordinator() (int64, error)
	RenewWorkerAssignments(string, []string, time.Time, time.Time) error
	WorkerResult(string) ([]byte, error)
	LookupWorkerAssignment(string, string) (*state.Assignment, error)
	RecordWorkerDiagnostic(string, []byte) error
	WorkerDiagnostic(string) ([]byte, error)
	AckWorkerCompletionWithoutResult(state.Assignment, string, int64) (bool, error)
}

// Coordinator implements the worker protocol over durable worker and dispatch state.
type Coordinator struct {
	Workspace string
	Context   context.Context
	store     dispatchStore
	Artifacts artifacts.Driver
	lease     time.Duration
	heartbeat time.Duration
	epoch     int64
	initErr   error
}

func NewCoordinator(store state.Store) *Coordinator {
	d, _ := store.(dispatchStore)
	c := &Coordinator{store: d, lease: 30 * time.Second, heartbeat: WorkerHeartbeatWindow}
	if d != nil {
		c.epoch, c.initErr = d.StartCoordinator()
	}
	return c
}

func (c *Coordinator) Register(req WorkerRegistration) (WorkerRegistrationResult, error) {
	if c.store == nil {
		return WorkerRegistrationResult{}, errors.New("worker dispatch storage unavailable")
	}
	if c.initErr != nil {
		return WorkerRegistrationResult{}, c.initErr
	}
	caps := make([]state.WorkerCapability, 0, len(req.Capabilities))
	for _, capability := range req.Capabilities {
		caps = append(caps, state.WorkerCapability{Name: capability.Name, Version: capability.Version})
	}
	if err := c.store.RegisterWorker(state.Worker{ID: req.WorkerID, Capacity: req.Capacity, Capabilities: caps, Labels: req.Labels, LastSeen: time.Now()}); err != nil {
		return WorkerRegistrationResult{}, err
	}
	return WorkerRegistrationResult{CoordinatorEpoch: c.epoch, HeartbeatSeconds: 10}, nil
}

func (c *Coordinator) Heartbeat(workerID string, req WorkerHeartbeat) (WorkerHeartbeatResult, error) {
	if c.store == nil {
		return WorkerHeartbeatResult{}, errors.New("worker dispatch storage unavailable")
	}
	if c.initErr != nil {
		return WorkerHeartbeatResult{}, c.initErr
	}
	workers, err := c.store.ListWorkers(time.Now().Add(-c.heartbeat))
	if err != nil {
		return WorkerHeartbeatResult{}, err
	}
	for _, w := range workers {
		if w.ID == workerID {
			w.LastSeen = time.Now()
			if err := c.store.RenewWorkerAssignments(workerID, req.Active, w.LastSeen, w.LastSeen.Add(c.lease)); err != nil {
				return WorkerHeartbeatResult{}, err
			}
			if err = c.store.RegisterWorker(w); err != nil {
				return WorkerHeartbeatResult{}, err
			}
			commands, err := c.heartbeatCommands(workerID, req.Active)
			if err != nil {
				return WorkerHeartbeatResult{}, err
			}
			return WorkerHeartbeatResult{CoordinatorEpoch: c.epoch, LeaseSeconds: int(c.lease.Seconds()), Commands: commands}, nil
		}
	}
	return WorkerHeartbeatResult{}, workerNotRegisteredError{}
}

func (c *Coordinator) heartbeatCommands(workerID string, active []string) ([]any, error) {
	if len(active) == 0 {
		return nil, nil
	}
	var commands []any
	for _, assignmentID := range active {
		a, err := c.store.LookupWorkerAssignment(workerID, assignmentID)
		if err != nil {
			return nil, err
		}
		if a == nil || a.State != "active" {
			commands = append(commands, map[string]any{"cancel": map[string]any{"assignmentId": assignmentID}})
			continue
		}
		if rs, ok := c.store.(interface {
			GetRun(string) (*state.Run, error)
		}); ok {
			run, err := rs.GetRun(a.RunID)
			if err != nil {
				return nil, err
			}
			if run != nil && run.State == "paused" {
				commands = append(commands, map[string]any{"pause": map[string]any{"assignmentId": assignmentID}})
			}
		}
	}
	return commands, nil
}

func (c *Coordinator) NextAssignment(workerID string) (*WorkerAssignment, error) {
	if c.store == nil {
		return nil, errors.New("worker dispatch storage unavailable")
	}
	if c.initErr != nil {
		return nil, c.initErr
	}
	workers, err := c.store.ListWorkers(time.Now().Add(-c.heartbeat))
	if err != nil {
		return nil, err
	}
	var worker *state.Worker
	for i := range workers {
		if workers[i].ID == workerID {
			worker = &workers[i]
			break
		}
	}
	if worker == nil {
		return nil, workerNotRegisteredError{}
	}
	d, generation, err := c.store.ClaimDispatch(workerID, time.Now().Add(c.lease))
	if err != nil || d == nil {
		return nil, err
	}
	return &WorkerAssignment{AssignmentID: d.AssignmentID, RunID: d.RunID, ActivationID: d.ActivationID, AttemptID: d.AttemptID, CoordinatorEpoch: c.epoch, Generation: generation, LeaseUntil: time.Now().Add(c.lease).UTC().Format(time.RFC3339), Request: json.RawMessage(d.Request)}, nil
}

func (c *Coordinator) Complete(workerID string, req WorkerCompletion) error {
	if c.store == nil {
		return errors.New("worker dispatch storage unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	result := workers.ExecuteResult{Outputs: req.Outputs, Logs: req.Logs, Usage: req.Usage}
	if req.Error != nil {
		result.Error = req.Error.Message
	}
	if req.State != "succeeded" && result.Error == "" {
		result.Error = "worker assignment " + req.State
	}
	if req.State == "succeeded" {
		if err := workers.VerifyOutputArtifacts(c.Artifacts, req.Outputs); err != nil {
			return invalidCompletionError{message: err.Error()}
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	assignment := state.Assignment{RunID: req.RunID, ActivationID: req.ActivationID, WorkerID: workerID, Generation: req.Generation, AssignmentID: req.AssignmentID, AttemptID: req.AttemptID}
	if req.CoordinatorEpoch != c.epoch {
		known, err := c.store.LookupWorkerAssignment(workerID, req.AssignmentID)
		if err != nil {
			return err
		}
		if known == nil || known.RunID != req.RunID || known.ActivationID != req.ActivationID || known.AttemptID != req.AttemptID || (known.Generation != req.Generation && !(known.State == "uncertain" && known.Generation == req.Generation+1)) {
			return staleAssignmentError{}
		}
		return c.recordDiagnosticCompletion(assignment, req.State, raw)
	}
	ok, err := c.store.CompleteWorkerAssignment(assignment, req.State, time.Now(), raw)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if req.State == "cancelled" {
		ok, err = c.store.AckWorkerCompletionWithoutResult(assignment, req.State, req.Generation)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	if req.State != "cancelled" {
		return c.recordDiagnosticCompletion(assignment, req.State, raw)
	}
	return staleAssignmentError{}
}

func (c *Coordinator) recordDiagnosticCompletion(a state.Assignment, terminalState string, raw []byte) error {
	if err := c.store.RecordWorkerDiagnostic(a.AttemptID, raw); err != nil {
		return err
	}
	ackState := terminalState
	if ackState == "succeeded" {
		ackState = "cancelled"
	}
	if _, err := c.store.AckWorkerCompletionWithoutResult(a, ackState, a.Generation); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) AppendWorkerLog(workerID string, req WorkerLogAppend) error {
	if c.store == nil {
		return errors.New("worker dispatch storage unavailable")
	}
	if c.initErr != nil {
		return c.initErr
	}
	if req.CoordinatorEpoch != c.epoch {
		return staleAssignmentError{}
	}
	chunks, ok := c.store.(interface {
		AppendLogChunk(state.LogChunk) error
	})
	if !ok {
		return errors.New("log storage unavailable")
	}
	assignments, err := c.store.ListAssignments(req.RunID)
	if err != nil {
		return err
	}
	active := false
	for _, a := range assignments {
		if a.WorkerID == workerID && a.AssignmentID == req.AssignmentID && a.AttemptID == req.AttemptID && a.Generation == req.Generation && a.State == "active" {
			active = true
			break
		}
	}
	if !active {
		return staleAssignmentError{}
	}
	return chunks.AppendLogChunk(state.LogChunk{
		RunID:        req.RunID,
		ActivationID: req.ActivationID,
		Stream:       req.Stream,
		Seq:          req.Seq,
		Data:         req.Data,
		Timestamp:    time.Now(),
	})
}

type workerNotRegisteredError struct{}

func (workerNotRegisteredError) Error() string { return "worker is not registered" }
func (workerNotRegisteredError) ProtocolError() (int, string, string) {
	return 409, CodeWorkerNotRegistered, "worker must register first"
}

type staleAssignmentError struct{}

func (staleAssignmentError) Error() string { return fmt.Sprintf("assignment generation is stale") }
func (staleAssignmentError) ProtocolError() (int, string, string) {
	return 409, CodeStaleAssignment, "assignment is no longer active"
}

type invalidCompletionError struct{ message string }

func (e invalidCompletionError) Error() string { return e.message }
func (e invalidCompletionError) ProtocolError() (int, string, string) {
	return 400, CodeInvalidCompletion, e.message
}

// EnqueueDispatch makes a generic execute request available to matching workers.
func (c *Coordinator) EnqueueDispatch(d state.Dispatch) error {
	if c.store == nil {
		return errors.New("worker dispatch storage unavailable")
	}
	return c.store.EnqueueDispatch(d)
}

func (c *Coordinator) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	if c.initErr != nil {
		return workers.ExecuteResult{}, c.initErr
	}
	if c.store == nil {
		return workers.ExecuteResult{}, errors.New("dispatch storage unavailable")
	}
	if req.RunID == "" || req.ActivationID == "" || req.AttemptID == "" {
		return workers.ExecuteResult{}, errors.New("remote execution requires workflow identity")
	}
	guarded, err := workers.GuardRequest(req)
	if err != nil {
		return workers.ExecuteResult{}, err
	}
	coordinatorlog.RegisterSecrets(guarded.Secrets)
	guarded = workers.PrepareRemoteDispatch(guarded)
	guarded = workers.StripInlineArtifactBytes(guarded)
	raw, err := json.Marshal(guarded)
	if err != nil {
		return workers.ExecuteResult{}, err
	}
	// isUncertain reports whether req.AttemptID has an uncertain or expired
	// active assignment, meaning a worker may already have executed the step.
	isUncertain := func() (bool, error) {
		assignments, listErr := c.store.ListAssignments(req.RunID)
		if listErr != nil {
			return false, listErr
		}
		for _, assignment := range assignments {
			if assignment.AttemptID == req.AttemptID && (assignment.State == "uncertain" || (assignment.State == "active" && time.Now().After(assignment.LeaseUntil))) {
				return true, nil
			}
		}
		return false, nil
	}

	read := func() (workers.ExecuteResult, bool, error) {
		raw, err := c.store.WorkerResult(req.AttemptID)
		if err != nil {
			return workers.ExecuteResult{}, false, err
		}
		if raw == nil {
			return workers.ExecuteResult{}, false, nil
		}
		var result workers.ExecuteResult
		err = json.Unmarshal(raw, &result)
		return result, true, err
	}
	if r, found, err := read(); found || err != nil {
		return r, err
	}
	// If the previous attempt has an uncertain or expired assignment, do NOT
	// enqueue a new dispatch — the worker may have already performed the side
	// effect. Instead, poll until the worker reconnects and delivers its result
	// or the context is cancelled. This satisfies the durable-resume contract:
	// the engine waits for reconciliation without replaying effectful work.
	uncertain, err := isUncertain()
	if err != nil {
		return workers.ExecuteResult{}, err
	}
	if !uncertain {
		if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: req.AttemptID, RunID: req.RunID, ActivationID: req.ActivationID, AttemptID: req.AttemptID, Executor: req.Executor, Request: raw}); err != nil {
			return workers.ExecuteResult{}, err
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	ctx := req.Context
	if ctx == nil {
		ctx = c.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return workers.ExecuteResult{}, ctx.Err()
		case <-ticker.C:
			if r, found, err := read(); found || err != nil {
				return r, err
			}
			stillUncertain, err := isUncertain()
			if err != nil {
				return workers.ExecuteResult{}, err
			}
			if stillUncertain {
				// Worker may already have run the step; wait for reconciliation.
				uncertain = true
				continue
			}
			if uncertain {
				// A completion can become visible between result and assignment
				// reads; check once more before releasing the held dispatch.
				if r, found, err := read(); found || err != nil {
					return r, err
				}
				if err := c.store.ReconcileUncertainDispatch(req.ActivationID); err != nil {
					return workers.ExecuteResult{}, err
				}
				if err := c.EnqueueDispatch(state.Dispatch{AssignmentID: req.AttemptID, RunID: req.RunID, ActivationID: req.ActivationID, AttemptID: req.AttemptID, Executor: req.Executor, Request: raw}); err != nil {
					return workers.ExecuteResult{}, err
				}
				uncertain = false
			}
		}
	}
}
