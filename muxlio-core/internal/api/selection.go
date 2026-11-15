package api

import (
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/workers"
)

// Selection is performed by the durable store in the same transaction that
// claims an assignment and reserves its execution capacity.
func (c *Coordinator) SelectAtAdmission() bool { return true }

func (c *Coordinator) prepareAgentSelection(req *workers.ExecuteRequest, reattaching bool) error {
	preferences := workers.AgentSelectionPreferences{}
	if req.AgentSelection != nil {
		preferences = *req.AgentSelection
	}
	preferences.HeartbeatSeconds = c.heartbeat.Seconds()
	req.AgentSelection = &preferences
	// A durable dispatch already passed compatibility checks. Worker liveness
	// can change during coordinator recovery without revoking queued intent.
	if reattaching {
		return nil
	}
	registered, err := c.store.ListWorkers(time.Now().Add(-c.heartbeat))
	if err != nil {
		return err
	}
	compatible := false
	for _, worker := range registered {
		if !worker.Draining && worker.Capacity > 0 && len(workers.CompatibleAgents(*req, worker)) > 0 {
			compatible = true
			break
		}
	}
	if !compatible {
		return fmt.Errorf("no available compatible agent satisfies the execution requirements")
	}
	return nil
}
