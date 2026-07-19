# ClickHouse connector

One configured ClickHouse instance can be a source, a sink, or both. Venator
uses the official `clickhouse-go/v2` driver with either the native or HTTP
protocol. The source executes the rule's SQL with a deadline, asks ClickHouse
to throw at `max_result_rows`, and independently enforces the same row cap in
the client. `max_result_bytes` and the client-side `query.maxBytes` provide the
corresponding byte boundary; connector limits cannot exceed the global runtime
limits.

The sink writes searchable scalar fields plus the complete canonical finding
JSON in `payload`. Apply [`schema.sql`](schema.sql), then configure `sink.table`
as `venator.findings`. A custom value can use either `table` or
`database.table`. Table names are restricted to ASCII SQL identifiers and every
component is quoted; arbitrary SQL is not accepted in this field.

`ReplacingMergeTree` uses the deterministic `finding_id` to collapse scheduler
retries during background merges. Stable hash partitions preserve that property
even when retries cross a month boundary. Use `FINAL` when a query requires
immediate deduplication before merges have completed; add a TTL for time-based
retention rather than changing to a time partition without considering its
cross-partition deduplication semantics.

TLS verifies certificates by default. `caFile` adds a private CA to the system
trust roots; `certFile` and `keyFile` enable mutual TLS. Keep
`insecureSkipVerify` for isolated development clusters only.
