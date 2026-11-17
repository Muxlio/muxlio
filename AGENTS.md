# Architecture boundaries

- Keep the framework domain-agnostic. The coordinator, workers, compiler, SDK
  primitives, storage, and generic UI must not know whether a workflow performs
  research, coding, reviews, or any other business task.
- Express execution behavior through generic predicates, dependencies, schemas,
  bounds, timeouts, retries, and explicit stop actions. Do not use workflow-domain
  classifications to select runtime behavior, validation rules, defaults, or
  success semantics.
- Keep domain-specific prompts, output shapes, acceptance criteria, progress
  definitions, and policies in recipes or user-authored workflows. Framework
  code must not interpret fields such as `review`, `approved`, or issue severity
  as special; workflows must declare the expressions or contracts to evaluate.
- Prefer existing generic primitives over new framework abstractions. If a
  capability cannot be explained without naming a workflow domain, implement it
  in the recipe first. Tests for framework behavior must use generic fixtures.
- Keep the coordinator, workers, and UI independent of specific adapters and
  providers.
- Put provider-specific protocols, event parsing, usage normalization, and
  provider limitations in the adapter that owns them.
- The coordinator stores and exposes the shared executor contract. Workers
  dispatch and execute that contract generically. The UI renders it generically.
  Do not branch on adapter names to change coordinator, worker, or UI behavior
  or user-facing explanations. Workers must not parse provider protocols or
  implement provider-specific authentication, environment, or output handling.
- Represent missing accounting as unavailable and a reported zero as zero.
  Keep collected readings distinct from estimates, preserve their provenance,
  and do not estimate token totals from credits or context-window measurements.
- Keep cross-adapter integration tests outside coordinator, worker, and UI
  internals; their unit tests should exercise the shared contract with generic
  fixtures.
