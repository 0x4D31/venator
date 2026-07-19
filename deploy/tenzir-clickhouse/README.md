# Tenzir integration

Tenzir is an optional collection and shaping layer, not a Venator dependency or
scheduler. There are two clean boundaries.

## Direct NDJSON boundary

For light local detection, let Tenzir read/filter a file and write bounded
NDJSON directly to Venator's built-in stdin source:

```sh
set -o pipefail # bash/zsh: preserve both Tenzir and Venator failures
tenzir 'from_file "/var/log/security/events.ndjson"
  where severity >= 3
  write_ndjson' |
venator run \
  --global-config config/files/global_config.yaml \
  --rule-config config/rules/example/single-stage-alert.yaml \
  --force
```

Venator writes canonical finding NDJSON to stdout in this example. The query is
one Tenzir argument; Venator deliberately does not expose a rule-defined shell
connector. That keeps command execution, filesystem permissions, and collection
outside the detection process.

## ClickHouse boundary

For retained local telemetry, run [`pipeline.tql`](pipeline.tql) in Tenzir to
append normalized events to ClickHouse. Venator then runs scheduled SQL using
`clickhouse.local-logs`, and writes canonical findings back through
`clickhouse.findings`.

```sh
export CLICKHOUSE_PASSWORD='replace-me' # Venator connector
export TENZIR_SECRETS__CLICKHOUSE_PASSWORD="$CLICKHOUSE_PASSWORD"
# From the repository root, keep this collector running in a separate terminal
# or under a service manager. watch=10s intentionally makes it long-lived.
tenzir -f deploy/tenzir-clickhouse/pipeline.tql
```

The included NDJSON fixture has exactly the destination columns `event_id`,
`timestamp`, `username`, `source_ip`, and `outcome`; the pipeline casts the RFC
3339 string to Tenzir's time type before appending. Adapt that normalization
boundary when your source uses names such as `user` or `event`. Every real
collector must provide a stable, source-unique `event_id`. This example requires Tenzir
Node 6.6 or newer because the table uses `DateTime64(6, 'UTC')` and
`LowCardinality(String)`; 6.6 introduced append support for existing columns
of those types. Other schemas may work with earlier releases.

`watch=10s` discovers newly appearing `*.ndjson` spool or rotated files. It is
not an offset-checkpointed tail of one continuously growing file, and restarting
the pipeline rereads matching files. The sample ClickHouse table is therefore a
`ReplacingMergeTree` keyed by `event_id`, and the detection query uses `FINAL`
so replayed IDs do not inflate the count. This is logical idempotency, not a
transactional handoff: retain or archive spool files according to your recovery
policy and monitor ingestion failures. Tenzir's `from_file` also supports
`remove` or `rename` after a file is read when that lifecycle fits your policy.
In another terminal, run the detector. The fixture uses fixed timestamps for
reproducibility, so update them or append current events to satisfy the sample
rule's 15-minute window:

```sh
export CLICKHOUSE_PASSWORD='replace-me'
venator run \
  --global-config config/examples/clickhouse-global.yaml \
  --rule-config config/examples/clickhouse-rule.yaml
```

Tenzir's current `from_clickhouse` maps scalar, nullable, UUID, DateTime64, and
array types but not ClickHouse `Map`. Venator therefore stores indexed scalars
and arrays plus the complete canonical envelope in a compressed `String`
payload. See the official [Tenzir ClickHouse operator](https://tenzir.com/docs/reference/operators/to_clickhouse/)
and [Tenzir releases](https://github.com/tenzir/tenzir/releases).

The included pipeline assumes Tenzir runs on the same host as the loopback-only
ClickHouse Compose stack and authenticates as its `venator` evaluation user.
Create separate least-privilege ingest/query users before production use.
