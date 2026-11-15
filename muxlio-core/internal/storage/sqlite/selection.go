package sqlite

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

// selectAnyDispatch runs inside the claim transaction. A busy candidate remains
// unresolved so the next free compatible worker can execute it without a retry.
func selectAnyDispatch(tx *sql.Tx, cfg state.AdmissionConfig, d state.Dispatch, req workers.ExecuteRequest, pollingWorker string, now time.Time) (state.Dispatch, bool, error) {
	preferences := workers.AgentSelectionPreferences{}
	if req.AgentSelection != nil {
		preferences = *req.AgentSelection
	}
	preferences.FailedExecutors = append([]string(nil), preferences.FailedExecutors...)
	prior, err := tx.Query(`SELECT executor FROM execution_attempts WHERE run_id=? AND activation_id=? AND (state IN ('failed','cancelled') OR (state='resolved' AND failure<>'')) AND id<>? ORDER BY retry_number,id`, d.RunID, d.ActivationID, d.AttemptID)
	if err != nil {
		return d, false, err
	}
	for prior.Next() {
		var name string
		if err := prior.Scan(&name); err != nil {
			prior.Close()
			return d, false, err
		}
		if !workers.IsAgent(name) {
			continue
		}
		preferences.FailedExecutors = append(preferences.FailedExecutors, name)
		preferences.LastFailedExecutor = name
	}
	err = prior.Err()
	prior.Close()
	if err != nil {
		return d, false, err
	}
	pinned := ""
	if req.IdempotencyKey != "" {
		err := tx.QueryRow(`SELECT executor FROM execution_attempts WHERE activation_id=? AND idempotency_key=? AND executor NOT IN ('any','agent/any') ORDER BY retry_number LIMIT 1`, d.ActivationID, req.IdempotencyKey).Scan(&pinned)
		if err != nil && err != sql.ErrNoRows {
			return d, false, err
		}
	}
	loads, workerLoads, turns := map[string]int{}, map[string]int{}, map[string]int64{}
	active, err := tx.Query(`SELECT d.executor,a.worker_id,SUM(COALESCE(r.slots,1)) FROM assignments a JOIN dispatches d ON d.attempt_id=a.attempt_id LEFT JOIN admission_reservations r ON r.activation_id=a.activation_id WHERE a.state='active' GROUP BY d.executor,a.worker_id`)
	if err != nil {
		return d, false, err
	}
	for active.Next() {
		var executor, worker string
		var slots int
		if err := active.Scan(&executor, &worker, &slots); err != nil {
			active.Close()
			return d, false, err
		}
		loads[executor] += slots
		workerLoads[worker] += slots
	}
	err = active.Err()
	active.Close()
	if err != nil {
		return d, false, err
	}
	rotation, err := tx.Query(`SELECT executor,turn FROM agent_selection_turns`)
	if err != nil {
		return d, false, err
	}
	for rotation.Next() {
		var name string
		var turn int64
		if err := rotation.Scan(&name, &turn); err != nil {
			rotation.Close()
			return d, false, err
		}
		turns[name] = turn
	}
	err = rotation.Err()
	rotation.Close()
	if err != nil {
		return d, false, err
	}
	window := 90 * time.Second
	if preferences.HeartbeatSeconds > 0 {
		window = time.Duration(preferences.HeartbeatSeconds * float64(time.Second))
	}
	registered, err := tx.Query(`SELECT id,capacity,capabilities_json,labels_json FROM workers WHERE draining=0 AND capacity>0 AND last_seen>=? ORDER BY id`, rfc3339(now.Add(-window)))
	if err != nil {
		return d, false, err
	}
	var candidates []workers.AgentCandidate
	requests := map[string][]byte{}
	for registered.Next() {
		var worker state.Worker
		var capsRaw, labelsRaw string
		if err := registered.Scan(&worker.ID, &worker.Capacity, &capsRaw, &labelsRaw); err != nil {
			registered.Close()
			return d, false, err
		}
		worker.Capabilities, err = decodeWorkerCapabilities(capsRaw)
		if err != nil {
			registered.Close()
			return d, false, err
		}
		if err := json.Unmarshal([]byte(labelsRaw), &worker.Labels); err != nil {
			registered.Close()
			return d, false, err
		}
		for _, name := range workers.CompatibleAgents(req, worker) {
			if pinned != "" && name != pinned {
				continue
			}
			demand := admissionDemand(cfg, name)
			if !admissionCanFit(cfg, demand, worker.Capacity) {
				continue
			}
			candidate := req
			candidate.Executor = name
			candidate.AgentSelection = nil
			candidate, err = workers.GuardRequest(candidate)
			if err != nil {
				continue
			}
			raw, err := json.Marshal(candidate)
			if err != nil {
				registered.Close()
				return d, false, err
			}
			concrete := d
			concrete.Executor = name
			concrete.Request = raw
			reason, err := admissionReason(tx, cfg, concrete, demand, worker.ID, worker.Capacity, now)
			if err != nil {
				registered.Close()
				return d, false, err
			}
			candidates = append(candidates, workers.AgentCandidate{Executor: name, WorkerID: worker.ID, Available: reason == "", Load: loads[name], WorkerLoad: workerLoads[worker.ID], Capacity: worker.Capacity, LastTurn: turns[name]})
			requests[name] = raw
		}
	}
	err = registered.Err()
	registered.Close()
	if err != nil {
		return d, false, err
	}
	choice, err := workers.ChooseAgent(candidates, &preferences)
	canClaim := false
	if err == nil && choice.Available {
		// Worker ID is only a deterministic ranking tie-breaker. An idle
		// polling worker offering the chosen agent need not wait for another
		// equally compatible worker to poll.
		for _, candidate := range candidates {
			if candidate.Executor == choice.Executor && candidate.WorkerID == pollingWorker && candidate.Available {
				canClaim = true
				break
			}
		}
	}
	if !canClaim {
		_, err := tx.Exec(`INSERT INTO admission_queue(activation_id,reason) VALUES(?,?) ON CONFLICT(activation_id) DO UPDATE SET reason=excluded.reason`, d.ActivationID, "Waiting for compatible agent capacity")
		return d, false, err
	}
	d.Executor = choice.Executor
	d.Request = requests[choice.Executor]
	return d, true, nil
}
