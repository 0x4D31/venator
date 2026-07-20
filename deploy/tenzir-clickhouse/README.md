# Optional Tenzir integration

This example demonstrates interoperability through Venator's generic NDJSON
and ClickHouse boundaries. Venator does not invoke, embed, or require Tenzir,
and an external scheduler still owns each bounded Venator run. Any producer
that satisfies the same finite-input or durable-store contract can replace it.

The examples cover two optional handoff patterns.

## Direct NDJSON boundary

For light local detection over a completed input, let Tenzir read a file and
write bounded NDJSON directly to Venator's built-in stdin source. The Venator
rule's CEL query selects high-severity events:

```sh
set -o pipefail # bash/zsh: preserve both Tenzir and Venator failures
tenzir 'from_file "/var/log/security/events.ndjson"
  write_ndjson' |
venator run \
  --global-config config/files/global_config.yaml \
  --rule-config config/rules/example/single-stage-alert.yaml \
  --force
```

This pipeline must reach EOF. Venator is not consuming a permanent Tenzir
stream: it publishes only after the bounded producer finishes. Preserve the
pipeline status so a failure in either process reaches the scheduler. A direct
pipe cannot roll back records already published if Tenzir emits partial output
and then fails; stage and validate the producer output first when successful
producer completion must be atomic with publication.

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
collector must provide a stable, source-unique `event_id`. The pipeline uses
Tenzir's current `from_file` and `to_clickhouse` syntax and appends to the
pre-created ClickHouse table rather than asking Tenzir to infer its schema.

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
rule's overlapping 20-minute window:

```sh
export CLICKHOUSE_PASSWORD='replace-me'
venator run \
  --global-config config/examples/clickhouse-global.yaml \
  --rule-config config/examples/clickhouse-rule.yaml
```

See the official [Tenzir ClickHouse integration](https://docs.tenzir.com/integrations/clickhouse/)
and [`from_file` reference](https://docs.tenzir.com/reference/operators/from_file/)
when adapting the sample to another Tenzir release or schema.

The included pipeline assumes Tenzir runs on the same host as the loopback-only
ClickHouse Compose stack and authenticates as its `venator` evaluation user.
Create separate least-privilege ingest/query users before production use.
