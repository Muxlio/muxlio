# Benchmarks

Muxlio includes a reference suite for checking workflow execution and saving
the raw results behind measurements. It is a tool you can run, not a published claim
that Muxlio or one agent provider is faster than another.

## What it checks

The suite covers sequential steps, parallel work, large artifacts, cancellation,
and recovery. Optional agent cases cover mixed-agent work, coding, and a workflow
authored by an agent. Each case has an independent correctness check.

Command-only cases exercise the execution engine without model calls. Agent
cases depend on the providers, models, machines, and settings you choose.

## What the results mean

Read correctness and completion results along with timing. Missing token or cost data stays unavailable; it is not zero.
The suite retains raw observations, plans, events, attempts, and verification
results so a measurement can be inspected.

No real-provider performance results are shipped with this suite. To run it,
understand its limits, or publish results, use the
[benchmark methodology](architecture/benchmark-methodology.md).
