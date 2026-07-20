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

The v0.1 form without `run` remains an alias. Both configuration paths are now
explicitly required; there is no working-directory-dependent default for the
global file. Add `--report-file` if automation needs a machine-readable run
summary. A disabled rule exits zero without querying; use `--force` for an
intentional ad-hoc run. Exit code 1 means a run started but failed; exit code 2
means the command, configuration, preflight, or local rule reference was
invalid. Findings on stdout remain canonical NDJSON; operational summaries
remain on stderr. JSON run reports distinguish source `queried`, CEL `matched`,
exclusion `excluded`, and final `findings` counts.

## Configuration

- Parsing is strict and validates enums, mappings, duplicate publishers, role
  configuration, durations, URLs, and ClickHouse table identifiers.
- YAML scalar and collection types are strict; quote string-looking values such
  as hexadecimal author names. Aliases remain valid as values but cannot be
  mapping keys, and YAML merge keys (`<<`) are rejected in global, rule, and
  exclusion documents; spell out the mapped keys instead.
- Connector instance keys now accept only ASCII letters, digits, hyphens, and
  underscores; dots remain the `<connector>.<instance>` separator.
- String values in connector instances and the global `llm` block may contain
  braced `${VARIABLE}` references. Expansion happens after YAML decoding and
  only when a rule selects that connector or reviewer. Instance keys, typed
  non-string fields, and rule fields are not interpolated; bare `$VARIABLE`
  remains literal.
- `runtime.maxBytes` defaults to 64 MiB alongside `runtime.maxRecords`; tune
  both for the scheduler memory limit and expected result shape.
- `stdin.default` and `file.ndjson` rules may define a CEL boolean `expr` over
  the current JSON object as `event`. Omit it for preselected candidates. It is
  invalid on database and search sources, where SQL or PPL remains the
  detection expression.
- `exclusionsPath` may be relative to the rule file for local deployments. Helm
  requires exactly `/app/exclusion/<file>.yaml`, where the file is packaged
  directly under `config/exclusions/`, and mounts it only for rules that
  reference it.
- `publishers` retains required-delivery semantics; `bestEffortPublishers` is
  optional. At least one sink across the two lists is required.

See the [rule and exclusion reference](rule-reference.md) for the complete
v0.2.0 syntax and operator semantics.

### OpenSearch sink index (breaking)

v0.1 hardcoded OpenSearch findings to the `signals` index. The v0.2.0 default is
`venator-findings-v1`, and the index is now configurable per OpenSearch instance
with `index`. Before upgrading, either install the supplied
[`venator-findings-v1` template](../connector/opensearch/index-template.json)
and migrate or intentionally start the new index, or set `index: signals` to
retain the old destination. Do not let a production upgrade silently split
findings across both names. Installation and pagination guidance is in the
[OpenSearch connector README](../connector/opensearch/README.md).

OpenSearch SQL pagination is now explicit. The default `sqlFetchSize: 0` keeps
aggregation and join rules compatible. Set a positive `sqlFetchSize` only on
connector instances used by basic SQL queries that need cursor pagination.
OpenSearch's `total` counts matching documents rather than promised output rows,
so Venator no longer rejects legitimate `LIMIT` or aggregation results when
`total` and `size` differ. Give nonpaged SQL and PPL rules explicit query bounds.

## Finding output

All publishers now receive a `venator.finding/v1` envelope rather than a mix of
raw maps and independently built signals. Update downstream schemas and parsers
for `id`, `run_id`, `detected_at`, `source`, `rule`, `attributes`, and `payload`.
`attributes` is omitted for raw findings without normalized signal fields. The
signal payload corrects `confidenceid` to `confidence_id`, omits unmapped
fields, and preserves TTP references and typed rule-specific data. Signal
mode is a projection: source fields that are not explicitly mapped are not
retained in its payload. Use raw mode when the complete source record is part
of the required evidence.

Finding IDs use the complete source record by default. For scanner or collector
records that contain a stable event key plus volatile fields, configure exact
top-level identity fields explicitly:

```yaml
identity:
  fields: [host_id, record_id]
```

Missing identity fields and collisions between different records in the same
run fail before publication. This is opt-in; existing rules retain whole-record
identity. Operators remain responsible for uniqueness across source
populations and runs sharing a rule UID. Changing an existing rule's identity
field set can change its finding IDs and redeliver unchanged evidence
downstream.

OpenSearch writes by stable finding ID. ClickHouse and BigQuery use documented
canonical finding schemas. Pub/Sub message attributes include schema, finding,
run, and rule IDs. Slack now renders a bounded human-readable summary and is not
a lossless archive; send canonical findings to an archival sink when retention
is required.

Slack uses `plain_text` Block Kit sections with bounded fields and a truncated
message or payload preview. `maxFindings` defaults to 20 and cannot exceed
Slack's 50-block ceiling. A batch above the configured limit is rejected; when
Slack is a required publisher, that rejection makes the run fail.

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
personal data. `maxFindings: 0` uses the default of 50. A required review must
cover every finding; if `maxFindings` truncates the batch, deterministic
findings are still published and the run then exits non-zero.

## Helm

The chart remains in `config/` and still creates one CronJob per enabled rule.
It requires Kubernetes 1.27 or newer because every CronJob sets `spec.timeZone`.
Run `helm template` against production rules before upgrade. The binary treats
`schedule` as opaque deployment metadata; Helm requires it for enabled rules
and Kubernetes validates its CronJob syntax. Review the new defaults for
`concurrencyPolicy: Forbid`, deadlines, backoff/history, non-root execution,
read-only root filesystem, and optional Secret references. Override security
settings only when a connector or sidecar genuinely requires it.

Every packaged rule must now have a non-empty YAML string UID. The chart rejects
duplicate UIDs among enabled rules so unrelated jobs cannot collide in
idempotent sinks.

The shipped smoke rule is disabled, so installing the chart cannot create a
green empty-stdin CronJob by accident. Enable real rules in your rule tree.
Completed Jobs have no TTL by default and remain subject to configurable
CronJob history limits. Remove the old no-op `replicaCount`, `nameOverride`,
and `fullnameOverride` keys from values files; the strict chart schema now
rejects them. `secretEnv` now defaults to an empty list instead of an optional
OpenSearch-password reference. Configure shared credentials explicitly, or use
`ruleSecretEnv` and `ruleEnvFrom` for per-rule isolation. New `serviceAccount`
and `ruleServiceAccounts` controls support GKE Workload Identity.
