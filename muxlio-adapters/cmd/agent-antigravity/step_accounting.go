package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/adapter"
	"strings"
)

type stepUsageUpdate struct {
	ConversationID string          `json:"conversation_id"`
	StepIndex      *int            `json:"step_index"`
	State          string          `json:"state"`
	TextDelta      string          `json:"text_delta"`
	Usage          json.RawMessage `json:"usage"`
}

func observeStepUsage(r *adapter.TokenRecorder, u stepUsageUpdate) error {
	if !strings.EqualFold(strings.TrimSpace(u.State), "DONE") {
		return nil
	}
	if u.ConversationID == "" || u.StepIndex == nil || *u.StepIndex < 0 {
		return fmt.Errorf("step usage identity unavailable")
	}
	id := fmt.Sprintf("step-%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d", u.ConversationID, *u.StepIndex))))
	return r.Delta(id, antigravityTokenCounters(u.Usage))
}

func antigravityTokenCounters(raw json.RawMessage) *adapter.TokenCounters {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil
	}
	u := adapter.ParseTokenUsage(raw)
	if u == nil {
		u = adapter.UnavailableTokenCounters()
	}
	for _, field := range []struct {
		key string
		dst **int64
	}{{"thinking_tokens", &u.Reasoning}, {"cache_write_tokens", &u.CacheWrite}, {"total_tokens", &u.Total}} {
		if b, ok := values[field.key]; ok {
			var n *int64
			if json.Unmarshal(b, &n) != nil || n == nil || *n < 0 {
				continue
			}
			*field.dst = n
		}
	}
	if !u.AnyReported() && u.Reasoning == nil && u.CacheWrite == nil && u.Total == nil {
		return nil
	}
	return u
}
