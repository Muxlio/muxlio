// Package telemetry exports privacy-conscious OpenTelemetry lifecycle spans and
// metrics. Instrumentation uses an allowlist of Muxlio identifiers, generic
// executor names, reported token counts and timings. Prompt bodies, params,
// inputs, transcripts, artifact contents, credentials and arbitrary error text
// are never added to spans, events or metrics. W3C propagation excludes baggage.
//
// Provider protocols and model/provider attributes belong to adapters; generic
// coordinator and worker instrumentation does not infer them from executor names.
package telemetry
