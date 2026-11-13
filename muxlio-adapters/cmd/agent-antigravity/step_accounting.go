package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-adapters/adapter"
	"math"
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
	usage, err := antigravityTokenCounters(u.Usage)
	if err != nil {
		return err
	}
	if usage == nil {
		return nil
	}
	if u.ConversationID == "" || u.StepIndex == nil || *u.StepIndex < 0 {
		return fmt.Errorf("step usage identity unavailable")
	}
	id := fmt.Sprintf("step-%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d", u.ConversationID, *u.StepIndex))))
	return r.Delta(id, usage)
}

func antigravityTokenCounters(raw json.RawMessage) (*adapter.TokenCounters, error) {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, nil
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
		return nil, nil
	}

	// Native agy usage reports uncached input. Its total is uncached input plus
	// output; cache_read_tokens is a separate consumed input quantity. Normalize
	// both to the shared inclusive contract. Legacy cached/cachedTokens aliases
	// retain their existing inclusive-input semantics.
	if _, native := values["cache_read_tokens"]; native {
		if cached, ok := u.ReportedCached(); ok {
			if input, ok := u.ReportedInput(); ok {
				if input > math.MaxInt64-cached {
					return nil, fmt.Errorf("antigravity input token count overflow")
				}
				u.Input = input + cached
			}
			if u.Total != nil {
				if *u.Total > math.MaxInt64-cached {
					return nil, fmt.Errorf("antigravity total token count overflow")
				}
				total := *u.Total + cached
				u.Total = &total
			}
		}
	}
	return u, nil
}
