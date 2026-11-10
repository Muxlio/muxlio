package main

import (
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

// mergeTerminalUsage applies event usage the same way as the Cursor adapter.
// Complete terminal totals replace any prior aggregate; partial terminal usage
// overlays only the dimensions present so a later sessionUpdate cannot drop
// counts already reported on an earlier update.
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
