package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/tiktoken-go/tokenizer"
)

// These are resource limits, not assumptions about model context windows.
const kiroEstimateMaxBytes = 32 << 20
const kiroEstimateMaxRequests = 10000

// cl100k_base is a reproducible proxy, not the served model's tokenizer.
// Keep its identity pinned in every reading, including historical readings.
func kiroEstimatedCount(n int64, source string) *contract.Reading[contract.Count] {
	return &contract.Reading[contract.Count]{Value: contract.Count(fmt.Sprint(n)), Source: source,
		Fidelity: "proxy", Method: "cl100k_base-bpe", Version: "tokenizer-0.7.0-kiro-content-v1"}
}

type kiroOutputEstimator struct {
	blocks    map[string]*strings.Builder
	tools     map[string]string
	bytes     int
	legacy    strings.Builder
	finalText string
	acpText   bool
}

func (e *kiroOutputEstimator) append(key, text string) {
	if text == "" || e.bytes+len(text) > kiroEstimateMaxBytes {
		return
	}
	if e.blocks == nil {
		e.blocks = make(map[string]*strings.Builder)
	}
	block := e.blocks[key]
	if block == nil {
		if len(e.blocks) >= kiroEstimateMaxRequests {
			return
		}
		block = &strings.Builder{}
		e.blocks[key] = block
	}
	block.WriteString(text)
	e.bytes += len(text)
}

func (e *kiroOutputEstimator) observe(kind string, raw json.RawMessage) {
	switch kind {
	case "sessionUpdate":
		var p struct {
			Kind    string          `json:"sessionUpdate"`
			ID      string          `json:"toolCallId"`
			Input   json.RawMessage `json:"rawInput"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Meta struct {
				Kiro struct {
					ReplayID string `json:"replayId"`
				} `json:"kiro"`
			} `json:"_meta"`
		}
		if json.Unmarshal(kiroSessionUpdateBody(raw), &p) != nil {
			return
		}
		switch p.Kind {
		case "agent_message_chunk", "agent_thought_chunk":
			if p.Content.Type != "text" {
				return
			}
			if p.Kind == "agent_message_chunk" && p.Content.Text != "" {
				e.acpText = true
			}
			e.append(p.Kind+":"+p.Meta.Kiro.ReplayID, p.Content.Text)
		case "tool_call", "tool_call_update":
			// rawInput updates replace prior arguments for this tool call.
			// Tool titles, status, results and reasoning signatures are not output.
			if p.ID == "" || len(p.Input) == 0 || string(p.Input) == "null" {
				return
			}
			var v any
			d := json.NewDecoder(bytes.NewReader(p.Input))
			d.UseNumber()
			if d.Decode(&v) != nil {
				return
			}
			b, err := json.Marshal(v)
			if err != nil {
				return
			}
			if e.tools == nil {
				e.tools = make(map[string]string)
			}
			if len(e.tools) >= kiroEstimateMaxRequests && e.tools[p.ID] == "" {
				return
			}
			if e.bytes-len(e.tools[p.ID])+len(b) > kiroEstimateMaxBytes {
				return
			}
			e.bytes += len(b) - len(e.tools[p.ID])
			e.tools[p.ID] = string(b)
		}
	case "text":
		var p struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(raw, &p) == nil && e.bytes+len(p.Content) <= kiroEstimateMaxBytes {
			e.legacy.WriteString(p.Content)
			e.bytes += len(p.Content)
		}
	case "runFinished":
		var p struct {
			Text string `json:"finalText"`
		}
		if json.Unmarshal(raw, &p) == nil && len(p.Text) <= kiroEstimateMaxBytes {
			e.finalText = p.Text
		}
	}
}

func (e *kiroOutputEstimator) count(enc tokenizer.Codec) (*int64, error) {
	var total int64
	observed := false
	add := func(text string) error {
		if text == "" {
			return nil
		}
		n, err := enc.Count(text)
		if err != nil {
			return err
		}
		total += int64(n)
		observed = true
		return nil
	}
	for _, block := range e.blocks {
		if err := add(block.String()); err != nil {
			return nil, err
		}
	}
	for _, text := range e.tools {
		if err := add(text); err != nil {
			return nil, err
		}
	}
	// Kiro can echo the same text in multiple event formats and at completion.
	if !e.acpText {
		text := e.legacy.String()
		if text == "" {
			text = e.finalText
		}
		if err := add(text); err != nil {
			return nil, err
		}
	}
	if !observed {
		return nil, nil
	}
	return &total, nil
}

// Count semantic content, not transport IDs, model names, image bytes or
// reasoning signatures. Each request includes its actual retained history, so
// repeated input and post-compaction histories are counted as sent.
func kiroRequestInput(raw []byte, enc tokenizer.Codec) (*int64, error) {
	var dump struct {
		Request struct {
			State struct {
				Current map[string]json.RawMessage   `json:"currentMessage"`
				History []map[string]json.RawMessage `json:"history"`
			} `json:"conversationState"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &dump); err != nil {
		return nil, err
	}
	var total int64
	observed := false
	add := func(text string) error {
		if text == "" {
			return nil
		}
		n, err := enc.Count(text)
		if err != nil {
			return err
		}
		total += int64(n)
		observed = true
		return nil
	}
	messages := append(dump.Request.State.History, dump.Request.State.Current)
	for _, message := range messages {
		for _, key := range []string{"userInputMessage", "assistantResponseMessage"} {
			var p struct {
				Content  string `json:"content"`
				ToolUses []struct {
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"toolUses"`
				Reasoning struct {
					Text string `json:"text"`
				} `json:"reasoningContent"`
				Context struct {
					Tools   json.RawMessage `json:"tools"`
					Results []struct {
						Content []struct {
							Text string          `json:"text"`
							JSON json.RawMessage `json:"json"`
						} `json:"content"`
					} `json:"toolResults"`
				} `json:"userInputMessageContext"`
			}
			if len(message[key]) == 0 {
				continue
			}
			if err := json.Unmarshal(message[key], &p); err != nil {
				return nil, err
			}
			for _, text := range []string{p.Content, p.Reasoning.Text} {
				if err := add(text); err != nil {
					return nil, err
				}
			}
			for _, tool := range p.ToolUses {
				if err := add(tool.Name); err != nil {
					return nil, err
				}
				if len(tool.Input) > 0 {
					var compact bytes.Buffer
					if err := json.Compact(&compact, tool.Input); err != nil {
						return nil, err
					}
					if err := add(compact.String()); err != nil {
						return nil, err
					}
				}
			}
			for _, raw := range []json.RawMessage{p.Context.Tools} {
				if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
					continue
				}
				var v any
				d := json.NewDecoder(bytes.NewReader(raw))
				d.UseNumber()
				if err := d.Decode(&v); err != nil {
					return nil, err
				}
				b, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}
				if err := add(string(b)); err != nil {
					return nil, err
				}
			}
			for _, result := range p.Context.Results {
				for _, content := range result.Content {
					if err := add(content.Text); err != nil {
						return nil, err
					}
					if len(content.JSON) > 0 {
						if err := add(string(content.JSON)); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	if !observed {
		return nil, nil
	}
	return &total, nil
}

func kiroCapturedInput(dir string, enc tokenizer.Codec) (*int64, error) {
	if dir == "" {
		return nil, nil
	}
	var total int64
	var count, size int
	observed := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || filepath.Ext(path) != ".json" {
			return nil
		}
		count++
		if count > kiroEstimateMaxRequests {
			return fmt.Errorf("request capture limit exceeded")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, int64(kiroEstimateMaxBytes-size)+1))
		_ = f.Close()
		if err != nil {
			return err
		}
		size += len(b)
		if size > kiroEstimateMaxBytes {
			return fmt.Errorf("request capture size exceeded")
		}
		n, err := kiroRequestInput(b, enc)
		if err != nil {
			return err
		}
		if n != nil {
			total += *n
			observed = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !observed {
		return nil, nil
	}
	return &total, nil
}

func recordKiroEstimates(r *accounting.Recorder, usage *adapter.TokenCounters, e *kiroOutputEstimator, dir string) error {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		return err
	}
	input, inputErr := kiroCapturedInput(dir, enc)
	output, err := e.count(enc)
	if err != nil {
		return err
	}
	tokens := adapter.CollectedTokenMeasures(usage, "kiro-cli")
	if tokens == nil {
		tokens = &contract.TokenMeasures{}
	}
	if input != nil {
		tokens.Input.Estimated = kiroEstimatedCount(*input, "kiro-request-capture")
	}
	if output != nil {
		tokens.Output.Estimated = kiroEstimatedCount(*output, "kiro-stream-content")
	}
	if input != nil || output != nil {
		if err := r.ReplaceScope("tokens", []accounting.Part{{Scope: "tokens", Entry: contract.AccountingEntry{ID: "tokens", Tokens: tokens}}}); err != nil {
			return err
		}
		if err := r.SetCoverage(contract.AccountingKey{Kind: "tokens"}, "partial", "tokenizer-proxy-incomplete"); err != nil {
			return err
		}
	}
	return inputErr
}
