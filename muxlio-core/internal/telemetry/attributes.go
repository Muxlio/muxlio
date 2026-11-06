package telemetry

import (
	"crypto/sha256"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
)

// correlationID preserves cross-process correlation without exporting scope
// contents: activation and attempt IDs can embed input-derived map keys.
func correlationID(id string) string {
	if id == "" {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id)))
}

// Muxlio correlation dimensions. These identify a single operation across the
// coordinator, one or more workers, and adapters, independent of provider.
const (
	// AttrRunID is the workflow run identifier.
	AttrRunID = "muxlio.run.id"
	// AttrActivationID is the materialized plan-node activation identifier.
	AttrActivationID = "muxlio.activation.id"
	// AttrAttemptID is the execution attempt identifier (stable across workers).
	AttrAttemptID = "muxlio.attempt.id"
	// AttrExecutor is the generic executor capability name dispatched to.
	AttrExecutor = "muxlio.executor"
	// AttrWorkerID is the worker that claimed and executed an assignment.
	AttrWorkerID = "muxlio.worker.id"
	// AttrNodeID is the plan node identifier within a subgraph.
	AttrNodeID = "muxlio.node.id"
)

// GenAI semantic-convention attributes (current, non-deprecated). The
// deprecated gen_ai.prompt / gen_ai.completion / gen_ai.system are intentionally
// not used. Content is excluded and the provider discriminator is adapter-owned.
const (
	// GenAIOperationName is the GenAI operation, e.g. invoke_workflow, execute_tool.
	GenAIOperationName = "gen_ai.operation.name"
	// GenAIAgentName names the application-defined workflow entrypoint.
	GenAIAgentName = "gen_ai.agent.name"
	// GenAIUsageInputTokens is reported input (prompt) token count.
	GenAIUsageInputTokens = "gen_ai.usage.input_tokens"
	// GenAIUsageOutputTokens is reported output (completion) token count.
	GenAIUsageOutputTokens = "gen_ai.usage.output_tokens"
	// GenAIUsageCacheReadTokens is reported cached input tokens served from cache.
	GenAIUsageCacheReadTokens = "gen_ai.usage.cache_read.input_tokens"
)

// Well-known GenAI operation names used by Muxlio.
const (
	// OpInvokeWorkflow names a full run (coordinator-owned lifecycle).
	OpInvokeWorkflow = "invoke_workflow"
	// OpExecuteTool names an executor invocation (tool/step execution).
	OpExecuteTool = "execute_tool"
)

// RunAttributes returns the Muxlio run-level span attributes.
func RunAttributes(runID, workflowName string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(AttrRunID, runID),
		attribute.String(GenAIOperationName, OpInvokeWorkflow),
	}
	if workflowName != "" {
		attrs = append(attrs, attribute.String(GenAIAgentName, workflowName))
	}
	return attrs
}

// ActivationAttributes returns activation-level span attributes.
func ActivationAttributes(runID, activationID, nodeID string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(AttrRunID, runID),
		attribute.String(AttrActivationID, correlationID(activationID)),
	}
	if nodeID != "" {
		attrs = append(attrs, attribute.String(AttrNodeID, nodeID))
	}
	return attrs
}

// AttemptAttributes returns attempt-level span attributes. Executor is the
// generic capability name (the dispatch target), never a provider model.
func AttemptAttributes(runID, activationID, attemptID, executor string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(AttrRunID, runID),
		attribute.String(AttrActivationID, correlationID(activationID)),
		attribute.String(AttrAttemptID, correlationID(attemptID)),
		attribute.String(GenAIOperationName, OpExecuteTool),
	}
	if executor != "" {
		attrs = append(attrs,
			attribute.String(AttrExecutor, executor),
			attribute.String("gen_ai.tool.name", executor),
		)
	}
	return attrs
}

// WorkerAttemptAttributes extends attempt attributes with the executing worker.
func WorkerAttemptAttributes(runID, activationID, attemptID, executor, workerID string) []attribute.KeyValue {
	attrs := AttemptAttributes(runID, activationID, attemptID, executor)
	if workerID != "" {
		attrs = append(attrs, attribute.String(AttrWorkerID, workerID))
	}
	return attrs
}
