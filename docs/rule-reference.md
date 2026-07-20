# Rule and exclusion reference

Venator reads one YAML document per rule and rejects unknown keys. A direct
invocation runs one rule once; scheduling belongs to the caller. The CLI does
not accept a rule directory: a scheduler or wrapper should start one process
per rule file so logs, deadlines, retries, reports, and exit statuses remain
independent. The Helm chart performs that expansion for packaged rule trees.

## Rule document

| Key | Constraint and meaning |
| --- | --- |
| `name` | Required, non-empty rule name. Helm additionally requires a unique DNS-1123 label of at most 52 characters; `global` is reserved. |
| `uid` | Required, non-empty stable identity namespace. It need not be a UUID, but changing it changes finding IDs. It must be unique among unrelated concurrently active rules. |
| `status` | Optional lifecycle metadata copied to findings. |
| `confidence` | Required: `unknown`, `low`, `medium`, or `high`. |
| `enabled` | Optional boolean; defaults to `false`. A disabled direct run is skipped unless `--force` is used. |
| `schedule` | Optional opaque deployment metadata. The binary neither parses nor executes it. Helm requires a non-empty Kubernetes CronJob schedule for every enabled packaged rule; Kubernetes validates that syntax. |
| `source` | Required source name. `stdin.default` is built in; configured sources use `<connector>.<instance>`, including `ndjson.<instance>`. |
| `language` | Required and operational. `stdin.default` and NDJSON instances require `CEL`; BigQuery and ClickHouse require `SQL`; OpenSearch accepts `SQL` or `PPL`. |
| `query` | Required non-empty query in the declared language. For a local NDJSON source it is a CEL boolean evaluated per event. Use the quoted string `"true"` to accept every input record. |
| `identity` | Optional finding-identity projection, described below. Omit it to hash the complete source record. |
| `publishers` | Required-delivery sink names. Every sink is attempted; any failure makes the run fail. |
| `bestEffortPublishers` | Best-effort sink names. Failures are reported but do not fail an otherwise successful run. At least one sink across the two publisher lists is required, and names cannot repeat. |
| `output` | Required output contract, described below. |
| `exclusionsFile` | Optional exclusion file. Relative paths resolve from the rule file's directory. |
| `llm` | Optional advisory-review policy, described below. |
| `author`, `description`, `references`, `tags`, `ttps` | Optional metadata. Each TTP may contain `framework`, `tactic`, `name`, `id`, and `reference`. |

Source and sink names must match a built-in or an instance declared with that
role in the global configuration. Use `venator validate` to check the rule,
connector references, local files, exclusions, and reviewer configuration
without issuing the detection query. Connector instance keys may contain only
ASCII letters, digits, hyphens, and underscores; dots are reserved as the
separator in `<connector>.<instance>`.

Global instances hold reusable source or sink configuration—locations,
credentials, transport, and safety limits—while a rule holds detection logic
and output mapping. The namespaced reference is uniform: for example,
`opensearch.production`, `clickhouse.home-lab`, or `ndjson.authentication`.

### Local NDJSON query

`stdin.default` and configured NDJSON sources select records with a
[Common Expression Language](https://cel.dev/) query. A finite file is a named
instance in the global configuration; its `path` may be absolute or relative
to the global configuration file:

```yaml
ndjson:
  instances:
    authentication-events:
      path: ./events.ndjson
```

The rule refers to that source profile. The only CEL variable is `event`, a map
containing the current JSON object:

```yaml
source: ndjson.authentication-events
language: CEL
query: |
  event.kind == "failed_login" &&
  has(event.severity) &&
  event.severity >= 4
```

The file must be a completed regular file. Each invocation reads it from byte
zero; Venator does not glob, tail, checkpoint, or remember offsets. `~` is not
expanded; use `${HOME}` explicitly when needed. The profile location belongs in
global configuration because it is source configuration, while the rule's
`query` remains detection logic. Profile string values support the same lazy
`${NAME}` expansion as other connector instances.

For bounded producer output, use the built-in stdin source without a profile:

```yaml
source: stdin.default
language: CEL
query: "true"
```

The CLI has no file-path override. Use a named profile when a file is part of
the deployment, and use `stdin.default` when a producer or agent chooses the
finite batch at invocation time. That keeps `validate` and `run` on the same
declarative source contract.

Identifier-like JSON keys support `event.key`; use bracket access for keys
containing dots, spaces, reserved words, or other punctuation, for example
`event["process.name"] == "loginwindow"`. CEL collection macros are
available, so a list can be tested with an expression such as
`event.tags.exists(tag, tag == "admin")`. JSON integers within CEL's ranges
become exact `int` or `uint` values. Decimal and exponent values become
IEEE-754 `double` values and may round; encode precision-sensitive values as
strings upstream. Numeric literals outside those CEL ranges remain their
original strings. Cross-type numeric comparisons are enabled.

CEL maps are unordered. Predicates must not depend on map iteration order.

The query is compiled during configuration validation and must have a boolean
result. Evaluation errors—including an unguarded missing field or an
incompatible comparison—fail the run before any finding is published.
Use `has(event.field)` when a key is optional. Venator limits the query to 4,096
code points, parser depth to 100, and runtime cost to 100,000 units per event.
Queries are stateless and cannot transform records, aggregate a batch,
correlate events, or maintain a time window.

CEL query evaluation runs after source size checks and before exclusions. Run
reports use `queried` for decoded source records, `matched` for events whose
query returned true, `excluded` for matched events suppressed by exclusions,
and `findings` for the final count.

### Finding identity

By default, a finding ID is derived from the rule UID and the complete source
record. Use `identity.fields` when a producer includes volatile metadata such
as a scan time or run ID alongside a stable event key:

```yaml
identity:
  fields: [host_id, record_id]
```

The list must be non-empty and contain unique, non-empty names. Each name is an
exact top-level source key; dots have no path-traversal meaning. Every selected
key must exist on every retained record at runtime. Include a host, tenant, or
other namespace when the event key is not globally unique.

Identity is evaluated after local query evaluation and exclusions. Different
retained records in the same run may not share one configured identity. A
collision fails before publication instead of making identity depend on source
order. Operators must still choose keys that are unique across every source
population and run sharing the rule UID. Byte-identical duplicate records
remain distinct and receive stable occurrence IDs. Identity controls the
envelope ID only; raw output still retains the complete record, and signal
output still follows its field mapping.
Changing the identity field set can change finding IDs and cause downstream
redelivery.

### Output

Raw output preserves each complete typed source record:

```yaml
output:
  format: raw
```

The `fields` key must be omitted or empty.

Signal output maps selected source columns into the normalized signal payload;
unmapped source fields are not retained. It requires at least one mapping,
every `source` must be non-empty, and a target may appear only once:

```yaml
output:
  format: signal
  fields:
    - field: Timestamp
      source: event_time
    - field: ActorUserName
      source: username
    - field: SrcIP
      source: source_ip
    - field: Message
      source: summary
```

Supported targets are `Timestamp`, `ActorUserName`, `ActorUserUID`,
`ResourceName`, `ResourceType`, `ResourceUID`, `SrcHostname`, `SrcIP`,
`DstHostname`, `DstIP`, `Message`, `EventID`, `EventIndex`, and
`RuleSpecificData`. `Timestamp`, when mapped, must be a `time.Time` value or an
RFC 3339 timestamp. Other textual targets accept values that can be represented
as text. `RuleSpecificData` preserves an object, parses a JSON object from text,
or stores another typed value under `raw`. Each `source` is an exact top-level
record key; dots have no path-traversal meaning.

### Advisory review

The rule-level `llm` block controls which deterministic findings are reviewed:

```yaml
llm:
  enabled: true
  required: false
  maxFindings: 25
  evidenceFields: [event, host, severity]
  redactFields: [host]
  prompt: Assess whether each finding deserves analyst attention based only on the supplied evidence.
```

An enabled review requires a non-empty prompt. `maxFindings: 0` uses the default
of 50; negative values are invalid. Evidence and redaction names select exact
top-level fields in the finding payload and must exist at runtime; dots have no
path-traversal meaning. When `evidenceFields` is non-empty, every
`redactFields` entry must also appear in that allowlist. With no
`evidenceFields`, the full payload is sent to the provider.

The model must return one annotation for every finding submitted for review.
`required: true` also requires review to be enabled and to cover every finding
from the run. If a required review fails or `maxFindings` truncates the batch,
deterministic findings are still delivered before the run exits non-zero.

Reviewer credentials and model selection live in the global configuration. The
bundled [local review rule](../config/examples/llm-review-rule.yaml) uses the
source profile in
[`llm-review-global.yaml`](../config/examples/llm-review-global.yaml), which
contains this reviewer block:

```yaml
llm:
  provider: openai
  apiKey: ${OPENAI_API_KEY}
  model: ${OPENAI_MODEL}
  timeout: 30s
```

`temperature` is optional. When omitted, Venator leaves it to the selected
model or provider; an explicitly configured value, including zero, is sent.

## Exclusion document

Reference the file from a rule with `exclusionsFile: ./exclusions.yaml`.
An exclusion file is a YAML list of up to 256 named CEL predicates. Every entry
has exactly two non-empty string fields: `name` and `when`. Names must be unique
in the file and at most 128 Unicode code points. A record is excluded when any
`when` query returns true.

```yaml
- name: test-user-from-known-address
  when: |
    has(event.username) &&
    event.username == "test" &&
    has(event.source_ip) &&
    event.source_ip in ["192.0.2.10", "192.0.2.11"]

- name: internal-service-account
  when: |
    has(event.actor) &&
    event.actor == "health-check@example.test"
```

`when` uses the same `event` value, type behavior, limits, and missing-field
rules as a local CEL query, regardless of the source's query language. This
keeps exclusions typed and makes nested objects, lists, boolean values, and
numeric comparisons available without a second operator language. Guard
optional fields with `has`. Connector-native values such as timestamps, bytes,
and precise decimals use their JSON representation inside CEL; raw findings
still retain the source record. Exclusion files are compiled during validation;
evaluation errors fail the run before any finding is published.

Local and non-Helm deployments may use absolute paths or paths relative to the
rule file; the target must be a regular file, not a pipe or device. In the
chart, put exclusion files directly under `config/exclusions/` and use exactly
`/app/exclusion/<name>.yaml`. The basename must be a lowercase DNS-1123 name
of at most 52 characters. Helm rejects relative, nested, or unpackaged
exclusion references.

## Environment references

Any string value inside a connector instance or the global `llm` block may
contain one or more braced `${NAME}` references. Connector instance keys,
typed boolean, numeric, and duration fields, and rule files are not
interpolated; bare `$NAME` remains literal. Expansion happens after YAML
decoding, so a secret value cannot change the YAML structure. An unset variable
fails when that connector or reviewer is selected, without requiring variables
for unrelated instances.

YAML uses strict scalar and collection types: quote string values that YAML
would otherwise parse as numbers or booleans. Anchors and aliases may reuse
values, but aliases cannot be mapping keys and merge keys (`<<`) are not
supported in global, rule, or exclusion documents.
