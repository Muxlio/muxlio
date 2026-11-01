package plan

import (
	"encoding/json"
	"fmt"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// unclaimedProver is implemented by stores that can prove a dispatch was never
// picked up by any worker, making it safe to replay.
type unclaimedProver interface {
	UnclaimedDispatch(activationID string) (bool, error)
}

// RecoverRuns reconstructs pinned engines for all non-terminal runs after a
// coordinator restart. The rules are:
//
//  1. Succeeded activations are preserved; their stored outputs are reused by
//     the resumed Engine.Run walk.
//  2. Failed or cancelled activations are terminal: the run cannot be safely
//     resumed and is transitioned to failed, requiring manual intervention.
//  3. Execute activations whose dispatch was already claimed by a worker
//     (uncertain after StartCoordinator) are left in their current state. The
//     resumed engine will poll via Coordinator.Execute until the worker
//     reconnects and delivers its result. No replay occurs.
//  4. Execute activations whose dispatch was never claimed (pending and
//     unassigned) are reset to queued so the engine can re-dispatch them.
//  5. Non-execute activations (project, join, branch, loop, map, child, wait)
//     that were interrupted mid-run are reset to queued; they are pure /
//     idempotent and safe to replay from scratch.
//
// A reboot must never require a human to restart the Engine.Run walk for the
// remaining graph.
func RecoverRuns(store state.Store, executor workers.Executor) (map[string]*Engine, error) {
	runs, err := store.ListRuns()
	if err != nil {
		return nil, err
	}
	engines := make(map[string]*Engine)
	for _, run := range runs {
		switch run.State {
		case RunSucceeded, RunFailed, RunCancelled:
			continue
		}
		fail := func(reason string) error {
			return store.SetRunError(run.ID, fmt.Errorf("muxlio: recovery requires manual reconciliation: %s", reason))
		}
		var p Plan
		if len(run.PlanJSON) == 0 {
			if err := fail("run has no pinned plan"); err != nil {
				return nil, err
			}
			continue
		}
		if err := json.Unmarshal(run.PlanJSON, &p); err != nil {
			if err := fail("pinned plan is invalid"); err != nil {
				return nil, err
			}
			continue
		}
		if err := p.VerifyDigest(); err != nil || p.Digest == "" || p.Digest != run.PlanDigest {
			if err := fail("pinned plan integrity check failed"); err != nil {
				return nil, err
			}
			continue
		}
		if errs := Validate(&p); hasErrors(errs) {
			if err := fail("pinned plan validation failed"); err != nil {
				return nil, err
			}
			continue
		}
		acts, err := store.ListActivations(run.ID)
		if err != nil {
			return nil, err
		}

		// Collect the set of execute node IDs across all subgraphs so we can
		// distinguish effectful nodes from pure structural ones.
		executeIDs := map[string]bool{}
		for _, sg := range p.Subgraphs {
			for _, node := range sg.Nodes {
				if node.Kind == KindExecute {
					executeIDs[node.ID] = true
				}
			}
		}

		prover, _ := store.(unclaimedProver)
		unsafe := false
		for _, act := range acts {
			switch act.State {
			case ActSucceeded:
				// Preserve; outputs already stored.
				continue
			case ActFailed, ActCancelled:
				// Terminal failure — we cannot know how much the node did
				// before it stopped. Mark the run for manual reconciliation.
				unsafe = true
			}
			if unsafe {
				break
			}
		}
		if unsafe {
			// Fail the run so observers can see it needs attention.
			for _, act := range acts {
				if act.State != ActSucceeded && act.State != ActFailed && act.State != ActCancelled {
					if err := store.SetActivationError(act.ID, fmt.Errorf("muxlio: execution outcome uncertain after restart; manual reconciliation required")); err != nil {
						return nil, err
					}
				}
			}
			if err := fail("execution interrupted; side effects may have occurred"); err != nil {
				return nil, err
			}
			continue
		}

		// Reset activations that are safe to replay. Execute activations with
		// uncertain dispatches are left as-is; the engine polls via
		// Coordinator.Execute until the worker delivers its result.
		for _, act := range acts {
			if act.State == ActSucceeded {
				continue
			}
			if executeIDs[act.NodeID] {
				// Determine whether the dispatch was ever claimed by a worker.
				claimed := true // assume claimed (conservative)
				if prover != nil {
					unclaimed, err := prover.UnclaimedDispatch(act.ID)
					if err != nil {
						return nil, err
					}
					claimed = !unclaimed
				}
				if claimed {
					// Dispatch was claimed (uncertain): leave the activation in
					// its current state so the engine polls for the result.
					// Do NOT reset or re-dispatch — the worker may already have
					// run its side effect.
					continue
				}
				// Dispatch was never claimed: safe to queue again.
				if err := store.SetActivationState(act.ID, ActQueued); err != nil {
					return nil, err
				}
			} else {
				// Non-execute (pure/structural) node: always safe to replay.
				if err := store.SetActivationState(act.ID, ActQueued); err != nil {
					return nil, err
				}
			}
		}

		// Transition the run back to running so Engine.Run can proceed.
		// Paused runs stay paused; created runs stay created.
		if run.State == RunRunning || run.State == RunWaiting {
			if _, err := store.TransitionRunState(run.ID, []string{RunRunning, RunWaiting}, RunRunning); err != nil {
				return nil, err
			}
		}

		engines[run.ID] = NewEngineWithStore(&p, executor, store)
	}
	return engines, nil
}
