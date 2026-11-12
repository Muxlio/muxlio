package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

// These are the documented Claude Code CLI result.modelUsage fields.
// The terminal result.usage object uses different, snake_case field names.
type claudeModelUsage struct {
	InputTokens              *int64 `json:"inputTokens"`
	OutputTokens             *int64 `json:"outputTokens"`
	CacheReadInputTokens     *int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens *int64 `json:"cacheCreationInputTokens"`
	ThinkingTokens           *int64 `json:"thinkingTokens"` // optional in older versions
}

type claudeResultUsage struct {
	OutputDetails *struct {
		Thinking *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
}

// claudeAccountingEntry returns a SINGLE invocation-scoped entry.
// modelUsage is preferred because result.usage excludes subagent consumption.
// When modelUsage is absent, result.usage is main-agent-only: the caller MUST
// NOT mark invocation token coverage complete without independent evidence.
func claudeAccountingEntry(ev map[string]json.RawMessage) (contract.AccountingEntry, error) {
	entry, tokenErr := claudeTokenEntry(ev)
	if raw, ok := ev["total_cost_usd"]; ok && string(raw) != "null" {
		amount, err := parseClaudeJSONAmount(raw)
		if err != nil {
			return entry, errors.Join(tokenErr, fmt.Errorf("claude result.total_cost_usd: %w", err))
		}
		entry.Costs = []contract.CostMeasure{{Currency: "USD", Basis: "unspecified", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: amount, Source: "claude-result", Fidelity: "provider-estimate"}}}}
	}
	return entry, tokenErr
}

func claudeTokenEntry(ev map[string]json.RawMessage) (contract.AccountingEntry, error) {
	entry := contract.AccountingEntry{ID: "invocation"}

	if raw, ok := ev["modelUsage"]; ok && string(raw) != "null" {
		var models map[string]claudeModelUsage
		if err := json.Unmarshal(raw, &models); err != nil {
			return entry, fmt.Errorf("claude result.modelUsage: %w", err)
		}
		if len(models) > 0 {
			if len(models) == 1 {
				for model := range models {
					entry.Model = model
				}
			}
			var input, output, cacheRead, cacheWrite, reasoning int64
			allThinking := true
			for name, model := range models {
				if model.InputTokens == nil || model.OutputTokens == nil || model.CacheReadInputTokens == nil || model.CacheCreationInputTokens == nil {
					return entry, fmt.Errorf("claude result.modelUsage[%q]: missing required counter", name)
				}
				for _, pair := range []struct {
					dst *int64
					val int64
				}{
					{&input, *model.InputTokens},
					{&output, *model.OutputTokens},
					{&cacheRead, *model.CacheReadInputTokens},
					{&cacheWrite, *model.CacheCreationInputTokens},
				} {
					if err := addClaudeCount(pair.dst, pair.val); err != nil {
						return entry, fmt.Errorf("claude result.modelUsage[%q]: %w", name, err)
					}
				}
				if model.ThinkingTokens == nil {
					allThinking = false
				} else if err := addClaudeCount(&reasoning, *model.ThinkingTokens); err != nil {
					return entry, fmt.Errorf("claude result.modelUsage[%q].thinkingTokens: %w", name, err)
				}
			}
			if err := addClaudeCount(&input, cacheRead); err != nil {
				return entry, err
			}
			if err := addClaudeCount(&input, cacheWrite); err != nil {
				return entry, err
			}
			if allThinking && reasoning > output {
				return entry, fmt.Errorf("claude result.modelUsage: thinkingTokens exceeds outputTokens")
			}
			total := input
			if err := addClaudeCount(&total, output); err != nil {
				return entry, err
			}
			const source = "claude-result-model-usage"
			tokens := &contract.TokenMeasures{
				Total:      claudeCollectedCount(total, source),
				Input:      claudeCollectedCount(input, source),
				Output:     claudeCollectedCount(output, source),
				CacheRead:  claudeCollectedCount(cacheRead, source),
				CacheWrite: claudeCollectedCount(cacheWrite, source),
			}
			tokens.Total.Collected.Fidelity = "derived-exact"
			if allThinking {
				tokens.Reasoning = claudeCollectedCount(reasoning, source)
			}
			entry.Tokens = tokens
		}
	}

	// Older/partial result messages can expose only result.usage. This is
	// main-loop usage, not whole-invocation usage when subagents were involved.
	if entry.Tokens == nil {
		if raw, ok := ev["usage"]; ok && string(raw) != "null" {
			var usage claudeResultUsage
			if err := json.Unmarshal(raw, &usage); err != nil {
				return entry, fmt.Errorf("claude result.usage: %w", err)
			}
			const source = "claude-result-main-loop"
			tokens := &contract.TokenMeasures{}
			var input, output int64
			inputKnown := usage.InputTokens != nil && usage.CacheReadInputTokens != nil && usage.CacheCreationInputTokens != nil
			if inputKnown {
				input = *usage.InputTokens
				if input < 0 {
					return entry, fmt.Errorf("claude result.usage.input_tokens: negative value")
				}
				if err := addClaudeCount(&input, *usage.CacheReadInputTokens); err != nil {
					return entry, err
				}
				if err := addClaudeCount(&input, *usage.CacheCreationInputTokens); err != nil {
					return entry, err
				}
				tokens.Input = claudeCollectedCount(input, source)
			}
			if usage.OutputTokens != nil {
				if *usage.OutputTokens < 0 {
					return entry, fmt.Errorf("claude result.usage.output_tokens: negative value")
				}
				output = *usage.OutputTokens
				tokens.Output = claudeCollectedCount(output, source)
			}
			if usage.CacheReadInputTokens != nil {
				if *usage.CacheReadInputTokens < 0 {
					return entry, fmt.Errorf("claude result.usage.cache_read_input_tokens: negative value")
				}
				tokens.CacheRead = claudeCollectedCount(*usage.CacheReadInputTokens, source)
			}
			if usage.CacheCreationInputTokens != nil {
				if *usage.CacheCreationInputTokens < 0 {
					return entry, fmt.Errorf("claude result.usage.cache_creation_input_tokens: negative value")
				}
				tokens.CacheWrite = claudeCollectedCount(*usage.CacheCreationInputTokens, source)
			}
			if inputKnown && usage.OutputTokens != nil {
				total := input
				if err := addClaudeCount(&total, output); err != nil {
					return entry, err
				}
				tokens.Total = claudeCollectedCount(total, source)
				tokens.Total.Collected.Fidelity = "derived-exact"
			}
			if usage.OutputDetails != nil && usage.OutputDetails.Thinking != nil {
				n := *usage.OutputDetails.Thinking
				if n < 0 || usage.OutputTokens != nil && n > *usage.OutputTokens {
					return entry, fmt.Errorf("invalid Claude reasoning subset")
				}
				tokens.Reasoning = claudeCollectedCount(n, source)
			}
			if usage.InputTokens != nil || usage.OutputTokens != nil || usage.CacheReadInputTokens != nil || usage.CacheCreationInputTokens != nil {
				entry.Tokens = tokens
			}
		}
	}

	return entry, nil
}

func claudeCollectedCount(n int64, source string) contract.Measure[contract.Count] {
	return contract.Measure[contract.Count]{Collected: &contract.Reading[contract.Count]{
		Value: contract.Count(strconv.FormatInt(n, 10)), Source: source, Fidelity: "metered",
	}}
}

func addClaudeCount(dst *int64, value int64) error {
	if value < 0 || *dst > math.MaxInt64-value {
		return fmt.Errorf("invalid or overflowing Claude token count")
	}
	*dst += value
	return nil
}

// Parse the JSON number exactly, including exponent notation (e.g. 1.3e-7).
// Never round-trip monetary values through float64. The shared ParseAmount
// enforces Muxlio's canonical decimal precision and magnitude limits.
func parseClaudeJSONAmount(raw json.RawMessage) (contract.Amount, error) {
	return accounting.ParseJSONAmount(raw)
}
