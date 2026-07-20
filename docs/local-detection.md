# Lightweight local detection

Venator does not require a SIEM or data lake. It does require bounded work: one
invocation reads a finite NDJSON batch or runs one bounded query, selects
records, builds canonical findings, attempts delivery, produces a run result,
and exits.

Venator is not a collector, long-running webhook server, event queue, or
general-purpose stream processor. Keeping those boundaries explicit makes a
local deployment small without inventing unsafe replay or checkpoint behavior.

## When Venator adds value

Use Venator when its rule-run contract matters:

- one independently scheduled, observable, and rerunnable job per rule;
- a bounded per-event CEL query for local NDJSON events;
- a versioned canonical finding schema and explicit evidence identity;
- exclusions and deterministic output mapping;
- required and best-effort sink fan-out with delivery receipts;
- optional bounded advisory AI review; and
- an exit status that a scheduler can use for retries and alerting.

An upstream collector owns collection and checkpoints. It can give Venator raw
JSON events for per-event selection or preselected scanner findings; Venator
then owns the rule result and delivery contract. If the upstream process
already provides every property you need, adding another component is
unnecessary.

## Choose the smallest boundary

| Need | Recommended boundary |
| --- | --- |
| Finite NDJSON event batch | `stdin.default` or a named `ndjson.<instance>` source with a CEL query |
| Preselected scanner findings | Local NDJSON source with `query: "true"` |
| Ad hoc per-event JSON test | Local NDJSON source with a CEL query |
| Continuously appended files, rotation, or journald | Checkpointing collector, then completed batches |
| Parsing, correlation, joins, or windowing | Upstream processor or query store, then bounded events |
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

An `ndjson.<instance>` profile accepts one regular-file path in global
configuration; operators must point it at a completed snapshot. A relative
path resolves from the global YAML. Venator reads from the beginning on every
run and does not remember offsets, expand globs, follow rotations, or wait for
appended records.

For both sources, the rule's required CEL `query` makes one boolean decision
per JSON object. For example, `has(event.severity) && event.severity >= 5`
selects records with a high numeric severity. Use the quoted string `"true"`
when every input object is already a candidate. Matching records then pass
through exclusions; the remainder become findings. A missing field or
incompatible type that the query does not handle fails the run before
publication.

This is deliberately smaller than a log-query engine. It does not parse text,
transform records, compare one event with another, aggregate, join, or keep a
time window. Perform those operations in the producer or a bounded database
query. The [rule reference](rule-reference.md#local-ndjson-query) defines the
query syntax and limits.

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

## Existing local detectors

Completed Bumblebee output and closed Stinger session reports can feed Venator.
Use a CEL expression when the decision depends only on fields in one JSON
record; otherwise select actual findings in a bounded upstream step. Project
stable, top-level identity fields when the producer exposes them. Do not point
an `ndjson.<instance>` profile at an append-only report; stage the batch first
when producer success must be atomic with publication.

SantaMon already provides detection, queueing, and delivery, so it should
normally send directly to its backend or automation receiver. Add Venator only
when its canonical finding, multi-sink delivery, or agent-handoff contract is
worth operating as a separate stage.

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
