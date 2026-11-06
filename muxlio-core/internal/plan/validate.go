package plan

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"math"
	"sort"
	"strings"

	"github.com/muxlio/muxlio-core/internal/workers"
	"time"
)

// ValidationError is a structured validation error with a stable code.
type ValidationError = diagnostic.Diagnostic

// hasErrors returns true when any ValidationError has severity "error" (not just warnings).
func hasErrors(errs []ValidationError) bool {
	return HasErrors(errs)
}

// Validate checks a plan for structural correctness. It returns a slice
// of errors; if the slice is empty the plan is valid.
func Validate(p *Plan) (errs []ValidationError) {
	defer func() { errs = finalizeDiagnostics(p, errs) }()
	if err := p.Limits.validate(); err != nil {
		errs = append(errs, ValidationError{Code: "MUXLIO_BAD_RUN_LIMITS", Severity: "error", Phase: "plan", Message: err.Error()})
	}
	if !validDuration(p.DeadlineSeconds) {
		errs = append(errs, ValidationError{Code: "MUXLIO_BAD_DEADLINE", Severity: "error", Phase: "plan", Message: "deadlineSeconds must be finite, nonnegative and fit a duration"})
	}

	if p.IRVersion == "" {
		errs = append(errs, ValidationError{
			Code: "MUXLIO_MISSING_IR_VERSION", Severity: "error", Phase: "plan",
			Message: "plan is missing irVersion",
			Fix:     "Set irVersion to \"1.0\".",
		})
	} else if err := CheckCompatibility(p.IRVersion); err != nil {
		errs = append(errs, ValidationError{
			Code: "MUXLIO_UNSUPPORTED_IR_VERSION", Severity: "error", Phase: "plan",
			Message: err.Error(),
			Fix:     fmt.Sprintf("Compile the workflow using Plan IR %s or upgrade the coordinator to a compatible release.", CurrentIRVersion),
		})
	}
	if p.Name == "" {
		errs = append(errs, ValidationError{
			Code: "MUXLIO_MISSING_NAME", Severity: "error", Phase: "plan",
			Message: "plan is missing name",
			Fix:     "Set a human-readable workflow name.",
		})
	}
	rootName := p.Root
	if rootName == "" {
		rootName = "root"
	}
	if _, ok := p.Subgraphs[rootName]; !ok {
		errs = append(errs, ValidationError{
			Code: "MUXLIO_MISSING_ROOT_SUBGRAPH", Severity: "error", Phase: "plan",
			Message: fmt.Sprintf("root subgraph %q not found in subgraphs", rootName),
			Fix:     "Add a subgraph with the root name or set plan.root to an existing subgraph.",
		})
		return errs // can't continue without a root
	}

	// Validate each subgraph.
	names := make([]string, 0, len(p.Subgraphs))
	for name := range p.Subgraphs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sg := p.Subgraphs[name]
		for _, node := range sg.Nodes {
			if node.Kind != "execute" {
				continue
			}
			var value any
			if len(node.Params) > 0 && json.Unmarshal(node.Params, &value) == nil && hasLiteralCredentials(value) {
				errs = append(errs, ValidationError{Code: "MUXLIO_LITERAL_CREDENTIAL", Severity: "error", Phase: "plan", Subgraph: name, NodeID: node.ID, Message: "literal credentials are forbidden in execution parameters", Fix: "Use secretRefs with an executionPolicy allowlist and operator-configured worker handles."})
			}
			if err := validateExecutionSecurity(node); err != nil {
				errs = append(errs, ValidationError{Code: "MUXLIO_EXECUTION_POLICY", Severity: "error", Phase: "plan", Subgraph: name, NodeID: node.ID, Message: "execution permissions or secret references are invalid", Fix: "Declare allowed secret names and an enforced isolation runtime for restricted attempts."})
			}
		}
		errs = append(errs, validateSubgraph(p, name, sg)...)
	}

	return errs
}

// HasErrors returns true if any issue has severity "error".
func HasErrors(errs []ValidationError) bool {
	for _, e := range errs {
		if e.Severity == "error" {
			return true
		}
	}
	return false
}

// ErrorsOnly returns only issues with severity "error".
func ErrorsOnly(errs []ValidationError) []ValidationError {
	var result []ValidationError
	for _, e := range errs {
		if e.Severity == "error" {
			result = append(result, e)
		}
	}
	return result
}

// WarningsOnly returns the non-fatal issues separately from errors.
func WarningsOnly(issues []ValidationError) []ValidationError {
	var warnings []ValidationError
	for _, issue := range issues {
		if issue.Severity == "warning" {
			warnings = append(warnings, issue)
		}
	}
	return warnings
}

// visibleOuterNodes returns node IDs from enclosing subgraphs that a nested
// body subgraph may reference (e.g. prepare before a map in the root).
func visibleOuterNodes(p *Plan, bodySG string) map[string]bool {
	vis := map[string]bool{}
	for id := range visibleOuterNodeDefinitions(p, bodySG, map[string]bool{}) {
		vis[id] = true
	}
	return vis
}

func visibleOuterNodeDefinitions(p *Plan, bodySG string, visiting map[string]bool) map[string]Node {
	vis := map[string]Node{}
	if visiting[bodySG] {
		return vis
	}
	visiting[bodySG] = true
	defer delete(visiting, bodySG)
	for _, sgName := range sortedKeys(p.Subgraphs) {
		sg := p.Subgraphs[sgName]
		for i := range sg.Nodes {
			node := &sg.Nodes[i]
			// Nested bodies inherit only outputs completed before invocation.
			// A branch cannot see its own result or later parent nodes either.
			if node.BodyRef == bodySG || node.ThenRef == bodySG || node.ElseRef == bodySG {
				before := map[string]bool{}
				for _, id := range nodeIDsBefore(sg.Nodes, node.ID) {
					before[id] = true
				}
				for _, n := range sg.Nodes {
					if before[n.ID] {
						vis[n.ID] = n
					}
				}
				for id, n := range visibleOuterNodeDefinitions(p, sgName, visiting) {
					vis[id] = n
				}
			}
		}
	}
	return vis
}

func nodeIDsBefore(nodes []Node, beforeID string) []string {
	order, err := topoSort(nodes)
	if err != nil {
		return nil
	}
	var ids []string
	for _, n := range order {
		if n.ID == beforeID {
			break
		}
		ids = append(ids, n.ID)
	}
	return ids
}

func validateSubgraph(p *Plan, sgName string, sg Subgraph) []ValidationError {
	var errs []ValidationError

	// Node IDs must be unique within the subgraph.
	seen := make(map[string]bool)
	for id := range visibleOuterNodes(p, sgName) {
		seen[id] = true
	}
	for index, node := range sg.Nodes {
		if err := workers.ValidateWorkspaceContract(node.WorkspaceMode, node.ConcurrencyKeys); err != nil {
			errs = append(errs, ValidationError{Code: "MUXLIO_BAD_WORKSPACE", Severity: "error", Phase: "plan", Subgraph: sgName, NodeID: node.ID, Message: err.Error()})
		}
		if node.ID == "" {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_NODE_ID", Severity: "error", Phase: "plan",
				Subgraph: sgName,
				Message:  "node is missing id",
				Source:   &diagnostic.Source{Pointer: fmt.Sprintf("/subgraphs/%s/nodes/%d/id", pointerPart(sgName), index)},
				Fix:      "Every node needs a stable id.",
			})
			continue
		}
		if seen[node.ID] {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_DUPLICATE_NODE_ID", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: fmt.Sprintf("duplicate node id %q in subgraph %q", node.ID, sgName),
				Source:  &diagnostic.Source{Pointer: fmt.Sprintf("/subgraphs/%s/nodes/%d/id", pointerPart(sgName), index)},
				Fix:     "Node IDs must be unique within a subgraph.",
			})
		}
		seen[node.ID] = true

		errs = append(errs, validateNode(p, sgName, node)...)
	}

	errs = append(errs, validateBindings(p, sgName, sg)...)

	return errs
}

func validDuration(v *float64) bool {
	return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v < float64(math.MaxInt64)/float64(time.Second))
}

func validateNode(p *Plan, sgName string, node Node) []ValidationError {
	var errs []ValidationError
	if !validDuration(node.TimeoutSeconds) || !validDuration(node.QueueTimeoutSeconds) {
		errs = append(errs, ValidationError{Code: "MUXLIO_BAD_TIMEOUT", Severity: "error", Phase: "plan", Subgraph: sgName, NodeID: node.ID, Message: "timeoutSeconds and queueTimeoutSeconds must be finite, nonnegative and fit a duration"})
	}
	if node.Retry != nil {
		switch node.Retry.Effect {
		case "", "retryable", "none", "pure", "idempotency-key", "idempotent-with-key", "reconcile", "reconcilable", "manual", "nonretryable":
		default:
			errs = append(errs, ValidationError{Code: "MUXLIO_BAD_EFFECT", Severity: "error", Phase: "plan", Subgraph: sgName, NodeID: node.ID, Message: "unknown retry effect policy"})
		}
	}
	if retry := node.Retry; retry != nil && (retry.MaxAttempts < 0 || retry.MaxAttempts > 100 || retry.BackoffSeconds < 0 || retry.MaxBackoffSeconds < 0) {
		errs = append(errs, ValidationError{Code: "MUXLIO_BAD_RETRY", Severity: "error", Phase: "plan", Subgraph: sgName, NodeID: node.ID, Message: "retry requires 0..100 maxAttempts and nonnegative backoff durations"})
	}

	switch node.Kind {
	case KindExecute:
		if node.Executor == "" {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_EXECUTOR", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "execute node is missing executor",
				Fix:     "Set the executor capability, e.g. \"agent/claude\".",
			})
		}
		// Warn when a scalar output port is never mentioned in the task prompt.
		// Agents won't know what to write to the output file if it isn't named.
		for port, decl := range node.Outputs {
			if node.Executor == "command" {
				continue // command outputs come from the process, not an agent prompt
			}
			if decl.Path != "" {
				continue // file artifact — path is explicit in the prompt
			}
			if node.Task == "" || !strings.Contains(node.Task, port) {
				errs = append(errs, ValidationError{
					Code: "MUXLIO_SCALAR_OUTPUT_NOT_IN_TASK", Severity: "warning", Phase: "plan",
					Subgraph: sgName, NodeID: node.ID,
					Message: fmt.Sprintf("scalar output port %q is not mentioned in the task description; the agent may not know what to write", port),
					Fix:     fmt.Sprintf("Reference %q explicitly in the task text, e.g. \"write the result to output field %q\".", port, port),
				})
			}
		}
		// Warn when a fallback executor duplicates the primary.
		for _, fb := range node.FallbackExecutors {
			if fb == node.Executor {
				errs = append(errs, ValidationError{
					Code: "MUXLIO_DUPLICATE_FALLBACK_EXECUTOR", Severity: "warning", Phase: "plan",
					Subgraph: sgName, NodeID: node.ID,
					Message: fmt.Sprintf("fallback executor %q is the same as the primary executor; it will never add value", fb),
					Fix:     "Remove the duplicate from fallbackExecutors.",
				})
			}
		}

	case KindMap:
		if node.Items == nil {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_MAP_ITEMS", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "map node is missing items expression",
				Fix:     "Set items to an expression evaluating to the collection.",
			})
		}
		if node.BodyRef == "" {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_MAP_BODY", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "map node is missing bodyRef",
				Fix:     "Set bodyRef to a subgraph name.",
			})
		} else if _, ok := p.Subgraphs[node.BodyRef]; !ok {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_BAD_SUBGRAPH_REF", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: fmt.Sprintf("map bodyRef %q not found in subgraphs", node.BodyRef),
				Fix:     "Add the referenced subgraph or fix the name.",
			})
		}

	case KindBranch:
		if node.Condition == nil {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_CONDITION", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "branch node is missing condition",
				Fix:     "Set condition to a boolean expression.",
			})
		}
		if node.ThenRef == "" {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_THEN_REF", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "branch node is missing thenRef",
				Fix:     "Set thenRef to a subgraph name.",
			})
		} else if _, ok := p.Subgraphs[node.ThenRef]; !ok {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_BAD_SUBGRAPH_REF", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: fmt.Sprintf("branch thenRef %q not found", node.ThenRef),
				Fix:     "Add the referenced subgraph or fix the name.",
			})
		}
		if node.ElseRef != "" {
			if _, ok := p.Subgraphs[node.ElseRef]; !ok {
				errs = append(errs, ValidationError{
					Code: "MUXLIO_BAD_SUBGRAPH_REF", Severity: "error", Phase: "plan",
					Subgraph: sgName, NodeID: node.ID,
					Message: fmt.Sprintf("branch elseRef %q not found", node.ElseRef),
					Fix:     "Add the referenced subgraph or fix the name.",
				})
			}
		}

	case KindLoop:
		if node.While == nil {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_WHILE", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "loop node is missing while condition",
				Fix:     "Set while to a boolean expression.",
			})
		}
		if node.BodyRef == "" {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_LOOP_BODY", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "loop node is missing bodyRef",
				Fix:     "Set bodyRef to a subgraph name.",
			})
		} else if _, ok := p.Subgraphs[node.BodyRef]; !ok {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_BAD_SUBGRAPH_REF", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: fmt.Sprintf("loop bodyRef %q not found", node.BodyRef),
				Fix:     "Add the referenced subgraph or fix the name.",
			})
		}
		if node.MaxIterations == nil {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_UNBOUNDED_LOOP", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "loop node is missing maxIterations",
				Fix:     "Set maxIterations to a hard upper bound.",
			})
		}

	case KindProject:
		if len(node.Inputs) == 0 {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_EMPTY_PROJECT", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: "project node has no inputs",
				Fix:     "Add at least one input binding to project.",
			})
		}

	case KindJoin:
		// join is validated through input bindings; nothing extra needed.

	case KindWait:
		if node.WaitFor != "" && node.WaitFor != "timer" && node.WaitFor != "signal" {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_UNKNOWN_WAIT_TYPE", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: fmt.Sprintf("wait node %q has unknown waitFor %q", node.ID, node.WaitFor),
				Fix:     "Set waitFor to \"timer\" or \"signal\", or leave empty for a pass-through wait.",
			})
		}
		if node.WaitFor == "timer" && node.TimerSeconds == nil {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_TIMER_SECONDS", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: fmt.Sprintf("wait node %q has waitFor=timer but no timerSeconds", node.ID),
				Fix:     "Set timerSeconds to the number of seconds to wait.",
			})
		}
		if node.WaitFor == "signal" && node.SignalName == "" {
			errs = append(errs, ValidationError{
				Code: "MUXLIO_MISSING_SIGNAL_NAME", Severity: "error", Phase: "plan",
				Subgraph: sgName, NodeID: node.ID,
				Message: fmt.Sprintf("wait node %q has waitFor=signal but no signalName", node.ID),
				Fix:     "Set signalName to the signal the node should wait for.",
			})
		}

	case KindChild:
		if node.ChildRef == "" {
			errs = append(errs, ValidationError{Code: "MUXLIO_MISSING_CHILD_REF", Severity: "error", Phase: "plan", Subgraph: sgName, NodeID: node.ID, Message: "child node is missing childRef", Fix: "Set childRef to a subgraph name."})
		} else if _, ok := p.Subgraphs[node.ChildRef]; !ok {
			errs = append(errs, ValidationError{Code: "MUXLIO_BAD_SUBGRAPH_REF", Severity: "error", Phase: "plan", Subgraph: sgName, NodeID: node.ID, Message: fmt.Sprintf("child childRef %q not found in subgraphs", node.ChildRef), Fix: "Add the referenced subgraph or fix the name."})
		}
		if node.MaxDepth == nil || *node.MaxDepth <= 0 {
			errs = append(errs, ValidationError{Code: "MUXLIO_UNBOUNDED_CHILD", Severity: "error", Phase: "plan", Subgraph: sgName, NodeID: node.ID, Message: "child node is missing a positive maxDepth bound", Fix: "Set maxDepth to a positive hard upper bound."})
		}

	default:
		errs = append(errs, ValidationError{
			Code: "MUXLIO_UNKNOWN_NODE_KIND", Severity: "error", Phase: "plan",
			Subgraph: sgName, NodeID: node.ID,
			Message: fmt.Sprintf("unknown node kind %q", node.Kind),
			Fix:     "Use one of: execute, map, branch, loop, join, wait, child, project.",
		})
	}

	return errs
}

// ValidateInput checks that the run input conforms to the plan's input schema.
func ValidateInput(p *Plan, input json.RawMessage) error {
	if len(p.InputSchema) == 0 {
		return nil
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(input)))
	dec.UseNumber()
	if !json.Valid(input) {
		return &ValueValidationFailure{Code: InputValidationFailed, Path: "input", Expected: p.InputSchema, Message: "input is not valid JSON"}
	}
	if err := dec.Decode(&value); err != nil {
		return err
	}
	if err := validateJSONValue(value, p.InputSchema, "input", InputValidationFailed); err != nil {
		return err
	}
	var schema map[string]any
	_ = json.Unmarshal(p.InputSchema, &schema)
	if err := validateUniqueInputIDs(value, schema, "input"); err != nil {
		return &ValueValidationFailure{Code: InputValidationFailed, Path: "input", Expected: p.InputSchema, Actual: input, Message: err.Error()}
	}
	return nil
}
