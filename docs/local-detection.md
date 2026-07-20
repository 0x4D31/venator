# Lightweight local detection

Venator does not require a SIEM or data lake. It does require bounded work: one
invocation reads a finite candidate set or runs one bounded query, builds
canonical findings, attempts delivery, produces a run result, and exits.

Venator is not a collector, long-running webhook server, event queue, or
general-purpose stream processor. Keeping those boundaries explicit makes a
local deployment small without inventing unsafe replay or checkpoint behavior.

## When Venator adds value

Use Venator when its rule-run contract matters:

- one independently scheduled, observable, and rerunnable job per rule;
- a versioned canonical finding schema and explicit evidence identity;
- exclusions and deterministic output mapping;
- required and best-effort sink fan-out with delivery receipts;
- optional bounded advisory AI review; and
- an exit status that a scheduler can use for retries and alerting.

An upstream collector, scanner, or query tool may select candidate events;
Venator is independently responsible for normalizing and delivering the
resulting detections. If the upstream process already provides every property
you need, adding another component is unnecessary.

## Choose the smallest boundary

| Need | Recommended boundary |
| --- | --- |
| Completed scanner output or NDJSON batch | `stdin.default` or `file.ndjson` |
| Ad hoc filtering across completed JSON files | Query or filter first, then emit finite NDJSON |
| Continuously appended files, rotation, or journald | Checkpointing collector, then completed batches |
| Parsing, correlation, or windowing | Upstream event processor, then bounded candidates |
| HTTP event ingestion | Narrow authenticated receiver with a durable queue |
| Filtering plus one alert destination | A single upstream pipeline may be sufficient |
| Retained, high-volume history | ClickHouse, OpenSearch, or another query store |

v0.2.0 has no native SQLite connector. Query an application-owned database
read-only in a bounded adapter, convert selected rows to NDJSON, and preserve
both exit statuses. Venator should not own or mutate that database.

## Finite input means finite

`stdin.default` reads until EOF. Findings are built and delivered only after
the producer closes the pipe. Do not use `tail -F ... | venator`: the pipe does
not reach EOF, so the run eventually times out without becoming a live
detector.

`file.ndjson` accepts a regular file; operators must point it at a completed
snapshot. It reads from the beginning on every run and does not remember
offsets, follow rotations, or wait for appended records.

For both sources, every record remaining after exclusions becomes a finding.
They do not evaluate a general match expression over arbitrary raw logs.
Filter upstream with a scanner, SQL query, or another bounded processor and
provide only candidate records.

Preserve both process exit statuses when using a pipe. That reports an upstream
failure but cannot undo findings published from partial output; stage and
validate the batch first when producer success must be atomic with publication.

## Continuous logs and durable spool files

Use a checkpointing collector when a growing file or journald needs durable
cursors, rotation handling, backpressure, and buffering. The collector choice
is independent of Venator; its acknowledgement and replay behavior must match
the deployment's durability requirements. A small durable handoff can use this
lifecycle:

1. Write a uniquely named temporary file on the destination filesystem.
2. Flush, sync, and close it, then atomically rename it to a `.ready` name on
   the same filesystem. Sync the containing directory when power-loss
   durability matters.
3. Let one wrapper atomically claim the oldest ready batch and feed it to a
   deliberately enabled `stdin.default` rule (or use `--force`) with a unique
   `--report-file`.
4. Put every destination whose acceptance gates acknowledgement in
   `publishers`, not `bestEffortPublishers`.
5. Archive or delete the claim only when Venator exits zero and the report says
   `status: succeeded`. A skipped rule also exits zero and is not an
   acknowledgement.
6. Retain failed claims and reclaim stale claims after a wrapper crash.

Without a durable spool, advance a collector checkpoint only after Venator
succeeds. With a durable spool, the collector can checkpoint after the batch is
durable, but the batch remains unacknowledged until required Venator sinks
succeed. This can provide at-least-once handoff across process crashes; power
loss guarantees also depend on the filesystem and sync behavior above.

Use `identity.fields` when a producer supplies a stable event key. Include its
host, tenant, or other namespace when the key is only locally unique. Replayed
batches then keep the same finding IDs. Sinks must still tolerate duplicates:
a retry can repeat one delivery that succeeded before another required sink
failed.

## Local producer compatibility

- Bumblebee emits finite NDJSON on stdout. Select only `record_type: finding`
  because `--findings-only` also emits a `scan_summary`. A host-scoped rule can
  use `[record_id]` for identity. For combined hosts, project nested
  `endpoint.device_id` (or a stable hostname fallback) upstream into a
  top-level `endpoint_id`, then use `[endpoint_id, record_id]`. Stage the output
  first if a failed scan must never publish partial results.
- Stinger's per-session `reports/<session-id>/events.ndjson` is suitable after
  the session closes. Select `trap.trigger` and `shim.trigger`, not every alert
  event, and scope its top-level `event.id` with the host or session. Its global
  file is append-only and needs a checkpointing collector.
- SantaMon already performs CEL detection, correlation, queueing, and HTTP
  shipping. It should normally deliver directly to its backend or an
  automation receiver. v0.2.0 has no direct SantaMon-to-Venator adapter; a
  bounded export or external receiver is required. Add Venator only when a
  separate canonicalization, multi-sink, or agent-handoff stage is worth
  operating.

Do not add Venator merely to put another process between an existing detector
and its only destination. A Raspberry Pi receiver also sees only data that the
endpoints explicitly forward to it; it does not make endpoint-local scanners
or traps network-wide.

## Triggering an investigation agent

A `webhook.<instance>` publisher sends each canonical finding to an automation
receiver. This is an outbound sink, not an inbound Venator server. A successful
HTTP response means that the receiver accepted the finding; it does not prove
that an investigation completed.

The receiver should verify authentication and the optional Standard Webhooks
signature and timestamp, deduplicate `webhook-id`, enqueue quickly, and
investigate asynchronously. Use `Venator-Finding-ID` for optional semantic
deduplication across scheduler runs. Treat finding evidence as untrusted data,
never as agent instructions. Give the agent allowlisted read-only tools by
default, narrow credentials, and explicit time, cost, and rate limits. Require
human approval for destructive or externally visible actions.

Desktop AI applications are not automatically webhook servers. Target a
documented API or a small local gateway; otherwise let the scheduler invoke a
supported agent CLI after Venator completes.

## Raspberry Pi and small hosts

On a 64-bit Raspberry Pi or other small arm64 Linux host, use a lightweight
checkpointing collector for continuous input and a systemd timer for bounded
Venator runs. Keep `runtime.maxRecords`, `runtime.maxBytes`, and timeouts
conservative, and run heavy investigation on another host.
