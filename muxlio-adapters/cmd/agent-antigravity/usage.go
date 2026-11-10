package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

func addTokenUsage(total, reported *adapter.TokenUsage) (*adapter.TokenUsage, error) {
	sum, err := contract.AddTokenUsage(total, reported)
	if err != nil {
		return total, err
	}
	return sum, nil
}

// mergeTerminalUsage applies result usage. Complete terminal totals replace any
// prior aggregate; partial terminal usage overlays only the dimensions present.
func mergeTerminalUsage(prior, terminal *adapter.TokenUsage) *adapter.TokenUsage {
	if terminal == nil {
		return prior
	}
	if prior == nil || terminal.Complete() {
		return terminal
	}
	var in, out, ca *int64
	if v, ok := terminal.ReportedInput(); ok {
		in = &v
	} else if v, ok := prior.ReportedInput(); ok {
		in = &v
	}
	if v, ok := terminal.ReportedOutput(); ok {
		out = &v
	} else if v, ok := prior.ReportedOutput(); ok {
		out = &v
	}
	if v, ok := terminal.ReportedCached(); ok {
		ca = &v
	} else if v, ok := prior.ReportedCached(); ok {
		ca = &v
	}
	return contract.NewTokenUsage(in, out, ca)
}

type stepUsageTracker struct {
	seen map[string]struct{}
}

func stepIdentity(conversationID string, stepIndex int) string {
	return conversationID + "\x00" + strconv.Itoa(stepIndex)
}

// accumulateStepUsage adds per-step usage once when a step reaches DONE.
// Repeated ACTIVE/DONE updates for the same conversation step are ignored.
func accumulateStepUsage(
	total *adapter.TokenUsage,
	tracker *stepUsageTracker,
	conversationID string,
	stepIndex int,
	state string,
	usageRaw json.RawMessage,
) (*adapter.TokenUsage, error) {
	if !strings.EqualFold(strings.TrimSpace(state), "DONE") {
		return total, nil
	}
	reported := adapter.ParseTokenUsage(usageRaw)
	if reported == nil {
		return total, nil
	}
	if tracker.seen == nil {
		tracker.seen = make(map[string]struct{})
	}
	key := stepIdentity(conversationID, stepIndex)
	if _, ok := tracker.seen[key]; ok {
		return total, nil
	}
	tracker.seen[key] = struct{}{}
	return addTokenUsage(total, reported)
}

func formatUsageErr(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("aggregating token usage: %v", err)
}
