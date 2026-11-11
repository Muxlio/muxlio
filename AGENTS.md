# Architecture boundaries

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
