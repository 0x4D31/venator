# Architecture

Venator is a deterministic one-shot rule runner with scheduler adapters around
it. The design follows five boundaries.

```mermaid
flowchart TD
  S["Scheduler adapter"] --> R["One rule run"]
  R --> Q["Typed source query"]
  Q --> D["Exclusions and deterministic transform"]
  D --> A["Optional advisory review"]
  A --> P["Canonical sink fan-out"]
  P --> O["Run report and exit status"]
```

## 1. Scheduling is an adapter

The binary does not keep a clock or run a daemon. Helm maps enabled rules and
their schedule metadata to Kubernetes CronJobs; launchd, systemd, Nomad, an AI
agent, or a human can invoke the same command. This retains GKE's useful job
history and debugging UI without making the engine depend on Kubernetes APIs.

## 2. Sources preserve types

`model.Record` is `map[string]any`. Sources retain nulls, booleans, numbers,
timestamps, lists, and nested values. Text conversion occurs only where a
signal mapping explicitly targets a textual field. Queries are bounded by
context deadlines and connector row/byte/response limits. `stdin.default` is a
pipeline boundary; `file.ndjson` reads one finite rule-relative snapshot. It is
not a tailer and owns no hidden offset.

## 3. The engine builds one finding

The engine applies exclusions, builds raw or signal output once, and wraps it
in `venator.finding/v1`. Raw output retains the complete typed source record;
signal output deliberately retains only mapped normalized fields. Every sink
gets that same object. A finding ID is the SHA-256 of the rule UID plus the
original source evidence, so a scheduler retry has the same identity even
though it has a new run ID. Byte-identical rows are retained and receive stable
occurrence IDs instead of being silently collapsed. Queries should still
project a stable event or aggregation key whenever the source has one. The rule
UID is the identity namespace: reuse it across edits to avoid re-alerting on
unchanged evidence, or change it when an edit should create a new alert lineage.

## 4. AI is untrusted advisory enrichment

Log evidence is untrusted model input. The reviewer receives capped JSON with
stable IDs, no tools, and a strict response schema. It can attach suspicious,
benign, or uncertain advice to known IDs. It cannot return replacement records
or remove deterministic findings. Failure is best effort unless a rule marks
review as required; even then findings are delivered before the run reports a
review failure.

## 5. Delivery is explicit

Entries in `publishers` use required-delivery semantics;
`bestEffortPublishers` entries do not fail an otherwise successful run. At least
one sink across the two lists is required. Sinks fan out concurrently, retain
configured receipt ordering, and are all attempted. Any required failure makes
the process exit non-zero.
This is at-least-once delivery: a retry can repeat a successful Slack or Pub/Sub
delivery after another sink failed. Stable IDs let idempotent sinks use upserts
or deduplication; the ClickHouse schema uses that ID as its replacement key.

## State boundary

v0.2.0 deliberately does not keep log data or a hidden scheduler database.
ClickHouse/OpenSearch/BigQuery own queryable history, and Tenzir or another
collector owns file watching and parsing. Direct NDJSON allows a local tool to
provide already filtered candidates. A future incremental file source should
checkpoint only after required sinks succeed and must make its retry semantics
explicit; it should not be smuggled into the query interface as a side effect.
