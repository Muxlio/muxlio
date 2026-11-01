package workers

import "encoding/json"

type remoteCommand struct {
	Cancel *remoteAssignmentCommand `json:"cancel,omitempty"`
	Pause  *remoteAssignmentCommand `json:"pause,omitempty"`
}

type remoteAssignmentCommand struct {
	AssignmentID string `json:"assignmentId"`
}

func remoteHeartbeatRevokesAssignment(assignmentID string, assignmentEpoch int64, hb remoteHeartbeatResult) bool {
	if hb.CoordinatorEpoch != 0 && hb.CoordinatorEpoch != assignmentEpoch {
		return true
	}
	for _, raw := range hb.Commands {
		var cmd remoteCommand
		if err := json.Unmarshal(raw, &cmd); err != nil {
			continue
		}
		if cmd.Cancel != nil && cmd.Cancel.AssignmentID == assignmentID {
			return true
		}
	}
	return false
}
