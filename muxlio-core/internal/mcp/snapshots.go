package mcp

import (
	"encoding/json"
	"net/url"
	"strconv"
)

func (s *server) toolGetSnapshots(args json.RawMessage) toolCallResult {
	var p struct {
		RunID        string `json:"runId"`
		ActivationID string `json:"activationId"`
		AttemptID    string `json:"attemptId"`
		Current      bool   `json:"current"`
		After        int    `json:"after"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if p.RunID == "" {
		return errResult("runId is required")
	}
	q := url.Values{"activationId": {p.ActivationID}, "attemptId": {p.AttemptID}, "current": {strconv.FormatBool(p.Current)}, "after": {strconv.Itoa(p.After)}, "limit": {strconv.Itoa(p.Limit)}}
	resp, err := s.api.get("/v1/runs/"+url.PathEscape(p.RunID)+"/snapshots", q)
	if err != nil {
		return errResult("coordinator request failed: " + err.Error())
	}
	return apiResultOrError(resp)
}
