# Changelog

## v0.2.0

v0.2.0 turns the original Kubernetes-focused runner into a scheduler-neutral
engine while retaining Helm-managed rule-per-CronJob deployments as a
first-class workflow.

### Added

- Canonical `venator.finding/v1` envelopes, stable finding IDs, run IDs, sink
  receipts, atomic JSON run reports, and required/best-effort sink semantics.
- Typed records throughout the source boundary.
- Official ClickHouse source/sink support with bounded queries, TLS/mTLS,
  native or HTTP protocols, and batch finding inserts.
- Built-in NDJSON stdin, finite-file source, and stdout sink for local agents,
  workstation snapshots, and Tenzir.
- Optional exact-field identity projections for producers whose records contain
  stable event keys alongside volatile scan or run metadata.
- Generic webhook delivery of canonical findings, with optional Standard
  Webhooks signing, for automation and asynchronous investigation handoff.
- Advisory structured AI review using the official OpenAI Go SDK and Responses
  API; AI can annotate but cannot replace or suppress findings.
- `run`, `validate`, and `version` commands with command-specific help,
  human-readable run outcomes on stderr, finding-only stdout, and documented
  scheduler-safe exit codes; legacy v0.1 flags remain accepted.
- Deployment assets for Helm/GKE, plain Kubernetes, launchd, systemd, Nomad,
  Docker Compose, local agents, ClickHouse, and Tenzir.
- Lightweight local-detection guidance for finite inputs, collector
  checkpoints, durable spool handoff, Raspberry Pi deployments, and the cases
  where Tenzir alone is the smaller solution.
- Hermetic connector regression tests and GitHub CI for tests, race detection,
  vet, static analysis, vulnerability scanning, static/container builds, Helm,
  and Kustomize rendering.
- A configurable OpenSearch sink index, defaulting to
  `venator-findings-v1`, with a versioned index template.

### Fixed

- Required publisher and required-review failures now return non-zero.
- Disabled rules no longer execute unless `--force` is supplied.
- BigQuery query-only instances can no longer panic as publishers, and typed
  timestamps survive signal mapping.
- Pub/Sub can no longer silently drop findings that fail serialization.
- OpenSearch checks bulk HTTP/item errors, closes bodies, honors cancellation,
  detects malformed rows, supports explicit SQL cursor pagination without
  breaking aggregation or join queries, and rejects PPL cursors it cannot
  safely manage.
- Slack uses context-aware bounded HTTP and renders an intentionally lossy,
  human-readable finding summary with `plain_text` Block Kit.
- BigQuery uses stable insert IDs and a configurable bytes-billed ceiling;
  OpenSearch bulk delivery is bounded and chunked; ClickHouse queries have
  independent server and client result limits and require least-privilege
  credentials as their authorization boundary.
- Global, rule, and exclusion YAML reject implicit scalar coercion, aliases in
  mapping keys, and merge keys; `and`/`or` ambiguity is rejected, and regular
  expressions are compiled once.
- Null source values no longer compare equal to empty strings in exclusions or
  satisfy negative text operators.
- Environment references are decoded safely and resolved only for selected
  connectors; literal dollar signs in credentials are preserved, and the
  Docker builder never copies runtime configuration or secrets.
- Active runs preflight sources and required sinks before querying; validation
  also checks best-effort sinks and finite input files.
- Finite file sources reject pipes and devices, stdout publication can be
  interrupted, and Pub/Sub rejects oversized messages before publishing any
  finding in the batch.
- Helm exclusion volumes are conditional and derived from the referenced file;
  CronJobs gain overlap, deadline, retry/history, security defaults, and
  global or per-rule certificate mounts without changing the rule-per-CronJob
  operating model. Invalid rule types, unknown rule-scoped value keys,
  duplicate or reserved names, and mounts that shadow Venator-managed paths
  fail chart rendering instead of creating invalid or colliding resources.
- Helm rejects empty or non-string rule UIDs and duplicate UIDs among enabled
  rules before unrelated detections can collide in idempotent sinks.

### Changed

- Module path is `github.com/0x4D31/venator`.
- Go 1.25.12 or newer is required so released binaries include current
  standard-library security fixes.
- Every sink receives the same pre-built finding rather than transforming raw
  query rows independently.
- Required and best-effort sinks fan out concurrently so one slow destination
  cannot starve another before the run deadline.
- The legacy `llm` block is an advisory reviewer, not an alert gate.
- The Helm chart no longer carries unused Deployment replica/name values or a
  default OpenSearch Secret reference; shared and per-rule credentials are
  opt-in.

See [the migration guide](docs/migration-v0.2.md) for compatibility details.
