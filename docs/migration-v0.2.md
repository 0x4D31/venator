# Migrating to v0.2.0

v0.2.0 keeps the one-rule invocation and Helm rule-directory workflow, but it
changes output and failure semantics. Test the branch with a non-production
sink before upgrading a scheduled fleet.

## Build and module

- Upgrade builders to Go 1.25.12 or newer; earlier Go 1.25 patch releases have
  known standard-library vulnerabilities in network paths Venator calls.
- Update imports from `github.com/nianticlabs/venator` to
  `github.com/0x4D31/venator` if you maintain downstream Go packages.
- Pin the image to `ghcr.io/0x4d31/venator:v0.2.0` or your immutable digest.

## CLI

The preferred command is:

```sh
venator run --global-config global.yaml --rule-config rule.yaml
```

The v0.1 form without `run` remains an alias. Add `--report-file` if automation
needs a machine-readable run summary. A disabled rule exits zero without
querying; use `--force` for an intentional ad-hoc run.

## Configuration

- Parsing is strict and validates enums, mappings, duplicate publishers, role
  configuration, durations, URLs, and ClickHouse table identifiers.
- `${VARIABLE}` references are expanded after YAML decoding and only when a
  rule selects that connector. A missing selected secret fails clearly without
  forcing unrelated credentials into every job.
- `runtime.maxBytes` defaults to 64 MiB alongside `runtime.maxRecords`; tune
  both for the scheduler memory limit and expected result shape.
- `exclusionsPath` may be relative to the rule file for local deployments. Helm
  continues to support `/app/exclusion/<file>.yaml` and mounts only rules that
  reference an exclusion file.
- `bestEffortPublishers` is optional. Existing `publishers` remain required.

## Finding output

All publishers now receive a `venator.finding/v1` envelope rather than a mix of
raw maps and independently built signals. Update downstream schemas and parsers
for `id`, `run_id`, `detected_at`, `source`, `rule`, `attributes`, and `payload`.
The signal payload corrects `confidenceid` to `confidence_id` and preserves TTP
references and typed rule-specific data.

OpenSearch writes by stable finding ID. ClickHouse and BigQuery use documented
canonical finding schemas. Pub/Sub message attributes include schema, finding,
run, and rule IDs. Slack renders the same canonical JSON.

Create or migrate the BigQuery destination with
[`connector/bigquery/schema.sql`](../connector/bigquery/schema.sql). BigQuery
streaming inserts use the finding ID for best-effort retry deduplication; retain
the ID as a downstream idempotency key because BigQuery does not guarantee
permanent deduplication.

## AI review

The YAML key remains `llm` for compatibility, but its behavior changes. Model
output is a structured annotation keyed by finding ID; it never becomes the
finding list. Replace old prompts that requested an array of replacement alerts
with a short assessment instruction. Optional keys:

```yaml
llm:
  enabled: true
  required: false
  maxFindings: 25
  evidenceFields: [message, metadata]
  redactFields: [metadata]
  prompt: Assess whether each supplied finding deserves analyst attention.
```

LLM review is an explicit external egress boundary: without `evidenceFields`,
the complete finding payload is sent to the configured provider. Use the
top-level allowlist and redaction controls for logs containing credentials or
personal data. A required review must cover every finding; if `maxFindings`
truncates the batch, deterministic findings are still published and the run
then exits non-zero.

## Helm

The chart remains in `config/` and still creates one CronJob per enabled rule.
Run `helm template` against production rules before upgrade. Review the new
defaults for `concurrencyPolicy: Forbid`, deadlines, backoff/history, non-root
execution, read-only root filesystem, and optional Secret references. Override
security settings only when a connector or sidecar genuinely requires it.

The shipped smoke rules are disabled, so installing the chart cannot create a
green empty-stdin CronJob by accident. Enable real rules in your rule tree.
Completed Jobs have no TTL by default and remain subject to configurable
CronJob history limits. `replicaCount`, `nameOverride`, and `fullnameOverride`
remain accepted for old values files (CronJobs do not consume them). New
`serviceAccount`, `ruleServiceAccounts`, `ruleSecretEnv`, and `ruleEnvFrom`
controls support GKE Workload Identity and per-rule credential isolation.
