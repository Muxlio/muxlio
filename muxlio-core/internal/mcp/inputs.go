package mcp

import (
	"encoding/json"
	"net/url"
)

func (s *server) toolInputRequest(name string, args json.RawMessage) toolCallResult {
	var p struct {
		RunID          string          `json:"runId"`
		RequestID      string          `json:"requestId"`
		IdempotencyKey string          `json:"idempotencyKey"`
		Response       json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errResult("invalid arguments: " + err.Error())
	}
	if p.RunID == "" {
		return errResult("runId is required")
	}
	path := "/v1/runs/" + url.PathEscape(p.RunID) + "/input-requests"
	if name == "get_input_requests" {
		resp, err := s.api.get(path, nil)
		if err != nil {
			return errResult(err.Error())
		}
		return apiResultOrError(resp)
	}
	if p.RequestID == "" || p.IdempotencyKey == "" || len(p.Response) == 0 {
		return errResult("requestId, idempotencyKey and response are required")
	}
	resp, err := s.api.post(path+"/"+url.PathEscape(p.RequestID), p.Response, p.IdempotencyKey)
	if err != nil {
		return errResult(err.Error())
	}
	return apiResultOrError(resp)
}
