# Kiro usage fixture

`credit-only.stream.jsonl` combines the completion metadata of a successful
local Kiro CLI 2.37.0 session with its ACP stream envelope. The successful
session's persisted `usage_summary` supplied `promptTurnSummaries`,
`elapsedTime`, and `status`. Session and request identifiers are omitted.
The context counters come from a separate captured CLI stream. The terminal
event completes the replay; this file is not a captured successful smoke run.

The completion reports credits, without input/output/cache token totals.
Context counters describe the context window, not cumulative token usage.
The fixture verifies that these values remain unavailable as tokens through
the adapter, persistence, and API. Optional numeric token-metadata shapes are
covered separately by parser tests, without claiming the current CLI emits
them on successful runs.
