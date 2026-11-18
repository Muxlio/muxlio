package plan

import (
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/coordinatorlog"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func (e *Engine) prepareExecutionAttempt(req workers.ExecuteRequest, node *Node, scope []string, prior []state.ExecutionAttempt, keyedExecutor string, retryNumber int) (workers.ExecuteRequest, time.Time, error) {
	admission, deferred := e.executor.(workers.AdmissionSelector)
	deferred = deferred && admission.SelectAtAdmission()
	if workers.IsAnyAgent(req.Executor) && !deferred {
		// Direct local execution has no durable worker queue. Serialize the load
		// snapshot and attempt admission so concurrent branches see each other.
		e.selectionMu.Lock()
		defer e.selectionMu.Unlock()
	}
	if workers.IsAnyAgent(req.Executor) {
		// Keyed replay stays in its deduplication domain; a recovered admitted
		// attempt also retains its concrete executor regardless of preferences.
		if keyedExecutor != "" {
			req.Executor = keyedExecutor
		}
		for _, previous := range prior {
			if previous.ID == req.AttemptID && !workers.IsAnyAgent(previous.Executor) {
				req.Executor = previous.Executor
				break
			}
		}
		if workers.IsAnyAgent(req.Executor) {
			history, err := e.store.ListExecutionAttempts(req.RunID, "")
			if err != nil {
				return req, time.Time{}, err
			}
			dependencies, err := e.selectionDependencies(req.RunID, node, scope, history)
			if err != nil {
				return req, time.Time{}, err
			}
			preferences, load := selectionPreferences(req.RunID, node, scope, history, dependencies)
			req.AgentSelection = preferences
			if !deferred {
				selector, ok := e.executor.(workers.ExecutorSelector)
				if !ok {
					return req, time.Time{}, fmt.Errorf("muxlio: executor does not support automatic agent selection")
				}
				req.Executor, err = selector.SelectExecutor(req, load)
				if err != nil {
					return req, time.Time{}, fmt.Errorf("muxlio: select agent: %w", err)
				}
			}
		}
	} else {
		resolved, err := e.resolveAvailableExecutor(req.Executor, node)
		if err != nil {
			return req, time.Time{}, err
		}
		req.Executor = resolved
	}
	guarded, err := workers.GuardSelectionRequest(req)
	if err != nil {
		return req, time.Time{}, fmt.Errorf("muxlio: execution policy: %w", err)
	}
	coordinatorlog.RegisterSecrets(guarded.Secrets)
	started := time.Now()
	status := "running"
	if _, queued := e.executor.(workers.QueuedExecutor); queued {
		status = "queued"
	}
	err = e.admitAttempt(state.ExecutionAttempt{ID: req.AttemptID, RunID: req.RunID, ActivationID: req.ActivationID, RetryNumber: retryNumber, Executor: req.Executor, State: status, StartedAt: started, Effect: req.Effect, IdempotencyKey: req.IdempotencyKey})
	return guarded, started, err
}

// resolveAvailableExecutor checks whether the already-selected executor can be
// admitted somewhere. FallbackExecutors are applied only by executeExecute's
// retry/session/effect rules before prepare; this must not switch executors.
func (e *Engine) resolveAvailableExecutor(executor string, node *Node) (string, error) {
	_ = node
	store, _ := e.store.(workers.AvailabilityStore)
	if store == nil || executor == "" || workers.IsAnyAgent(executor) {
		return executor, nil
	}
	now := time.Now()
	if workerID := e.knownAvailabilityWorkerID(); workerID != "" {
		u, err := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: workerID, Executor: executor})
		if err != nil {
			return "", err
		}
		if !state.AvailabilityBlocksAdmission(u, now) {
			return executor, nil
		}
		mismatch := workers.AvailabilityMismatch(u)
		return "", fmt.Errorf("%w: %s", state.ErrExecutorUnavailable, mismatch.Message)
	}
	blocked, err := e.namedExecutorBlockedOnAllWorkers(store, executor, now)
	if err != nil {
		return "", err
	}
	if blocked == nil {
		return executor, nil
	}
	mismatch := workers.AvailabilityMismatch(blocked)
	return "", fmt.Errorf("%w: %s", state.ErrExecutorUnavailable, mismatch.Message)
}

// knownAvailabilityWorkerID returns the executing worker identity when prepare
// already knows which binding will run (local ScriptExecutor). Empty means
// claim-time selection among registered workers.
func (e *Engine) knownAvailabilityWorkerID() string {
	if holder, ok := e.executor.(interface{ AvailabilityWorkerID() string }); ok {
		return holder.AvailabilityWorkerID()
	}
	return ""
}

func (e *Engine) availabilityWorkerID() string {
	if id := e.knownAvailabilityWorkerID(); id != "" {
		return id
	}
	return "local"
}

type workerRegistry interface {
	ListWorkers(since time.Time) ([]state.Worker, error)
}

// namedExecutorBlockedOnAllWorkers reports a representative block when every
// registered worker that advertises the executor is unavailable. No capable
// workers means prepare stays open so claim can wait for registration.
func (e *Engine) namedExecutorBlockedOnAllWorkers(store workers.AvailabilityStore, executor string, now time.Time) (*state.ExecutorUnavailability, error) {
	registry, ok := e.store.(workerRegistry)
	if !ok {
		// In-memory fixtures without a worker registry keep the historical
		// "local" binding used by unit tests.
		u, err := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: "local", Executor: executor})
		if err != nil {
			return nil, err
		}
		if state.AvailabilityBlocksAdmission(u, now) {
			return u, nil
		}
		return nil, nil
	}
	workersList, err := registry.ListWorkers(now.Add(-24 * time.Hour))
	if err != nil {
		return nil, err
	}
	var firstBlocked *state.ExecutorUnavailability
	capable := 0
	for _, w := range workersList {
		if w.ID == "" || !workerAdvertises(w, executor) {
			continue
		}
		capable++
		u, lookupErr := store.GetExecutorUnavailability(state.ExecutorBinding{WorkerID: w.ID, Executor: executor})
		if lookupErr != nil {
			return nil, lookupErr
		}
		if !state.AvailabilityBlocksAdmission(u, now) {
			return nil, nil
		}
		if firstBlocked == nil {
			firstBlocked = u
		}
	}
	if capable == 0 {
		return nil, nil
	}
	return firstBlocked, nil
}

func workerAdvertises(w state.Worker, executor string) bool {
	for _, cap := range w.Capabilities {
		if cap.Name == executor {
			return true
		}
	}
	return false
}

func selectionPreferences(runID string, node *Node, scope []string, history []state.ExecutionAttempt, dependencies map[string]bool) (*workers.AgentSelectionPreferences, map[string]int) {
	p := &workers.AgentSelectionPreferences{LastUsed: map[string]int64{}}
	load := map[string]int{}
	act := activationID(runID, scope, node.ID)
	lastFailure := -1
	for _, a := range history {
		if !workers.IsAgent(a.Executor) {
			continue
		}
		if a.State == "running" || a.State == "queued" || a.State == "awaiting_reconciliation" {
			load[a.Executor]++
		}
		if stamp := a.StartedAt.UnixNano(); stamp > p.LastUsed[a.Executor] {
			p.LastUsed[a.Executor] = stamp
		}
		if a.ActivationID == act && (a.State == "failed" || a.State == "cancelled" || (a.State == "resolved" && a.Failure != "")) {
			p.FailedExecutors = append(p.FailedExecutors, a.Executor)
			if a.RetryNumber >= lastFailure {
				p.LastFailedExecutor = a.Executor
				lastFailure = a.RetryNumber
			}
		}
		if dependencies[a.ActivationID] && a.State == "succeeded" {
			p.PreviousExecutors = append(p.PreviousExecutors, a.Executor)
		}
	}
	return p, load
}
