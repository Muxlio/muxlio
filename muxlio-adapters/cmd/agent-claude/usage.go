package main

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

// parseClaudeTokenUsage maps Anthropic / Claude Code usage into the shared
// contract. Provider input_tokens is uncached input only; cache_read_input_tokens
// and cache_creation_input_tokens are folded into Input so attempt totals match
// billed prompt tokens. Cached still carries cache read when reported.
func parseClaudeTokenUsage(raw json.RawMessage) *adapter.TokenUsage {
	base := adapter.ParseTokenUsage(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return base
	}
	readCount := func(keys ...string) (int64, bool, bool) {
		for _, key := range keys {
			b, ok := values[key]
			if !ok {
				continue
			}
			var n *int64
			if json.Unmarshal(b, &n) != nil || n == nil || *n < 0 {
				return 0, false, true
			}
			return *n, true, true
		}
		return 0, false, false
	}

	uncached, uncachedOK, _ := readCount("input_tokens", "uncachedInputTokens", "inputTokens")
	cacheRead, cacheReadOK, cacheReadPresent := readCount("cache_read_input_tokens", "cache_read_tokens", "cacheReadInputTokens", "cacheReadTokens")
	cacheCreate, cacheCreateOK, cacheCreatePresent := readCount("cache_creation_input_tokens", "cacheCreationInputTokens")
	if cacheReadPresent && !cacheReadOK || cacheCreatePresent && !cacheCreateOK {
		return nil
	}

	if !uncachedOK || (!cacheReadOK && !cacheCreateOK) {
		return base
	}

	input := uncached
	if cacheReadOK {
		var err error
		input, err = addTokenCount(input, cacheRead)
		if err != nil {
			return base
		}
	}
	if cacheCreateOK {
		var err error
		input, err = addTokenCount(input, cacheCreate)
		if err != nil {
			return base
		}
	}

	var out, cached *int64
	if base != nil {
		if v, ok := base.ReportedOutput(); ok {
			out = &v
		}
		if !cacheReadOK {
			if v, ok := base.ReportedCached(); ok {
				cached = &v
			}
		}
	}
	if out == nil {
		if v, ok, _ := readCount("output_tokens", "outputTokens", "candidates_token_count", "completion_tokens"); ok {
			out = &v
		}
	}
	if cacheReadOK {
		cached = &cacheRead
	}

	in := input
	return contract.NewTokenUsage(&in, out, cached)
}

func addTokenCount(total, add int64) (int64, error) {
	if add < 0 {
		return 0, fmt.Errorf("count must be non-negative")
	}
	if total > math.MaxInt64-add {
		return 0, fmt.Errorf("token count overflow")
	}
	return total + add, nil
}
