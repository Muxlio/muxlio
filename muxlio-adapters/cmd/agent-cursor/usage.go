package main

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/muxlio/muxlio-adapters/adapter"
)

// parseCursorTokenUsage maps Cursor agent stream-json usage into the shared
// contract. inputTokens is uncached prompt tokens; cacheReadTokens and
// cacheWriteTokens are folded into Input so totals match billed prompt tokens.
// Cached still carries cache read when reported.
func parseCursorTokenUsage(raw json.RawMessage) *adapter.TokenCounters {
	result := parseCursorCounters(raw)
	if result == nil {
		result = adapter.UnavailableTokenCounters()
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	for _, key := range []string{"cache_creation_input_tokens", "cacheCreationInputTokens", "cacheWriteTokens"} {
		if b, ok := values[key]; ok {
			var n *int64
			if json.Unmarshal(b, &n) != nil || n == nil || *n < 0 {
				return nil
			}
			result.CacheWrite = n
			break
		}
	}
	if !result.AnyReported() && result.CacheWrite == nil {
		return nil
	}
	return result
}

func parseCursorCounters(raw json.RawMessage) *adapter.TokenCounters {
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
	cacheWrite, cacheWriteOK, cacheWritePresent := readCount("cache_creation_input_tokens", "cacheCreationInputTokens", "cacheWriteTokens")
	if cacheReadPresent && !cacheReadOK || cacheWritePresent && !cacheWriteOK {
		return nil
	}

	if !uncachedOK || (!cacheReadOK && !cacheWriteOK) {
		return base
	}

	input := uncached
	if cacheReadOK {
		var err error
		input, err = addUsageCount(input, cacheRead)
		if err != nil {
			return cursorUsageWithoutInput(base)
		}
	}
	if cacheWriteOK {
		var err error
		input, err = addUsageCount(input, cacheWrite)
		if err != nil {
			return cursorUsageWithoutInput(base)
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
	return adapter.NewTokenCounters(&in, out, cached)
}

// cursorUsageWithoutInput retains independently representable dimensions when
// the uncached and cached input counts cannot be combined into an int64.
func cursorUsageWithoutInput(base *adapter.TokenCounters) *adapter.TokenCounters {
	var out, cached *int64
	if base != nil {
		if v, ok := base.ReportedOutput(); ok {
			out = &v
		}
		if v, ok := base.ReportedCached(); ok {
			cached = &v
		}
	}
	return adapter.NewTokenCounters(nil, out, cached)
}

func addUsageCount(total, add int64) (int64, error) {
	if add < 0 {
		return 0, fmt.Errorf("count must be non-negative")
	}
	if total > math.MaxInt64-add {
		return 0, fmt.Errorf("token count overflow")
	}
	return total + add, nil
}

var mergeTerminalUsage = adapter.OverlayTokenCounters
