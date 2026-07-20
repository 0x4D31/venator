# Venator v0.2.0 deployment assets

Venator is a one-shot program: one process evaluates one rule, publishes its
findings, and exits. The scheduler is deliberately outside the binary. A rule's
`schedule` field is metadata for deployment tooling; invoking the
binary directly runs the rule immediately.

| Environment | Scheduler | Asset |
| --- | --- | --- |
| Laptop or workstation | Manual or an AI agent | [`local/`](local/) and [`agent/`](agent/) |
| Linux host | systemd timer | [`systemd/`](systemd/) |
| macOS user session | launchd agent | [`launchd/`](launchd/) |
| Container host | Docker Compose plus a host scheduler | [`docker-compose/`](docker-compose/) |
| Home lab with retained logs | ClickHouse Compose | [`clickhouse/`](clickhouse/) |
| Local collection and shaping | Tenzir + NDJSON/ClickHouse | [`tenzir-clickhouse/`](tenzir-clickhouse/) |
| Nomad cluster | Periodic batch job | [`nomad/`](nomad/) |
| Kubernetes cluster | Helm or CronJob/Kustomize | [`../config/`](../config/) and [`kubernetes/`](kubernetes/) |

## Common configuration contract

Every deployment supplies two read-only YAML files:

- a global connector configuration, passed with `--global-config`; and
- one rule configuration, passed with `--rule-config`.

Store credentials outside those files where possible. String values in selected
connector instances and the global reviewer expand `${VARIABLE}` references
lazily after YAML decoding, so systemd environment files, Nomad/Vault
templates, Kubernetes Secrets, and the invoking agent's environment can inject
credentials without changing a rule.

The normal invocation is:

```sh
venator \
  run \
  --global-config /etc/venator/files/global_config.yaml \
  --rule-config /etc/venator/rules/example.yaml
```

Run a separate scheduled job for each rule. Keep schedules non-overlapping when
a data source or sink is not idempotent, and set an execution deadline in the
scheduler. A non-zero exit status means the run did not complete successfully
and should be retried or alerted on.

[`examples/`](examples/) is a runnable finite `file.ndjson` snapshot used by the
host schedulers, so their example jobs do real work without waiting on stdin.
Production schedules normally use ClickHouse, OpenSearch, BigQuery, or a finite
spool produced by a collector with explicit checkpoint semantics. If Tenzir
supplies stdin, schedule a fixed wrapper pipeline and preserve both processes'
exit statuses.

## Container image

Release examples use `ghcr.io/0x4d31/venator:v0.2.0`. Until that image is
published, build the same tag locally:

```sh
VERSION=0.2.0 VENATOR_IMAGE=ghcr.io/0x4d31/venator:v0.2.0 \
  sh scripts/build_image.sh
```

The Dockerfile supports multi-architecture BuildKit builds. Its final image
contains only the static binary, CA roots, and timezone data, and runs as the
unprivileged numeric user `65532`.
