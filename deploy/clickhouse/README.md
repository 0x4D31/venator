# ClickHouse home-lab deployment

This Compose stack starts ClickHouse 26.3 LTS, creates the canonical Venator
findings table plus a small authentication source table, and provides an
ephemeral Venator service. It is intended for evaluation and home labs; use
separate least-privilege reader and writer accounts in production.

```sh
cd deploy/clickhouse
export CLICKHOUSE_PASSWORD='replace-with-a-long-random-value'
docker compose up -d clickhouse

docker compose exec -T clickhouse clickhouse-client \
  --user venator --password "$CLICKHOUSE_PASSWORD" \
  --query "INSERT INTO security.authentication
    SELECT concat('demo-', toString(number)), now(), 'alice',
      '203.0.113.10', 'failure' FROM numbers(10)"

docker compose --profile run run --rm venator
```

The sample detection threshold is ten failures, so insert at least ten rows to
produce a finding. The authentication fixture uses `event_id` as its
`ReplacingMergeTree` key and the detection query uses `FINAL`; replaying a
collector spool after a restart therefore does not double-count a stable event
ID.
Query stored envelopes with:

```sh
docker compose exec clickhouse clickhouse-client \
  --user venator --password "$CLICKHOUSE_PASSWORD" \
  --query 'SELECT finding_id, rule_name, payload FROM venator.findings FINAL'
```

The password is required by Compose and is never given a repository default.
The ports bind to loopback only. Add TLS, network isolation, backups, resource
limits, and distinct users before treating this as a production data plane.
