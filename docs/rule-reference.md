# Rule and exclusion reference

Venator reads one YAML document per rule and rejects unknown keys. A direct
invocation runs one rule once; scheduling belongs to the caller. The CLI does
not accept a rule directory: a scheduler or wrapper should start one process
per rule file so logs, deadlines, retries, reports, and exit statuses remain
independent. The Helm chart performs that expansion for packaged rule trees.

## Rule document

| Key | Constraint and meaning |
| --- | --- |
| `name` | Required, non-empty rule name. Helm additionally requires a unique DNS-1123 label of at most 52 characters. |
| `uid` | Required, non-empty stable identity namespace. It need not be a UUID, but changing it changes finding IDs. It must be unique among unrelated concurrently active rules. |
| `status` | Optional lifecycle metadata copied to findings. |
| `confidence` | Required: `unknown`, `low`, `medium`, or `high`. |
| `enabled` | Optional boolean; defaults to `false`. A disabled direct run is skipped unless `--force` is used. |
| `schedule` | Optional opaque deployment metadata. The binary neither parses nor executes it. Helm requires a non-empty Kubernetes CronJob schedule for every enabled packaged rule; Kubernetes validates that syntax. |
| `queryEngine` | Required source name. Built-ins are `stdin.default` and `file.ndjson`; configured sources use `<connector>.<instance>`. |
| `language` | Required. `stdin.default` and `file.ndjson` require `NDJSON`; BigQuery and ClickHouse require `SQL`; OpenSearch accepts `SQL` or `PPL`. |
| `query` | Source-specific input. It is required except for `stdin.default`, where it must be omitted or empty. For `file.ndjson`, it is a completed NDJSON-file path; a relative path is resolved from the rule file's directory. |
| `expr` | Optional non-empty CEL boolean evaluated for each `stdin.default` or `file.ndjson` event. Omit it when the input already contains only candidates. It is invalid for other sources. |
| `identity` | Optional finding-identity projection, described below. Omit it to hash the complete source record. |
| `publishers` | Required-delivery sink names. Every sink is attempted; any failure makes the run fail. |
| `bestEffortPublishers` | Best-effort sink names. Failures are reported but do not fail an otherwise successful run. At least one sink across the two publisher lists is required, and names cannot repeat. |
| `output` | Required output contract, described below. |
| `exclusionsPath` | Optional exclusion file. Relative paths resolve from the rule file's directory. |
| `llm` | Optional advisory-review policy, described below. |
| `author`, `description`, `references`, `tags`, `ttps` | Optional metadata. Each TTP may contain `framework`, `tactic`, `name`, `id`, and `reference`. |

Source and sink names must match a built-in or an instance declared with that
role in the global configuration. Use `venator validate` to check the rule,
connector references, local files, exclusions, and reviewer configuration
without issuing the detection query. Connector instance keys may contain only
ASCII letters, digits, hyphens, and underscores; dots are reserved as the
separator in `<connector>.<instance>`.

### Local NDJSON expression

The two local NDJSON sources can select records with a
[Common Expression Language](https://cel.dev/) predicate. The only variable is
`event`, a map containing the current JSON object:

```yaml
queryEngine: file.ndjson
language: NDJSON
query: ./events.ndjson
expr: |
  event.kind == "failed_login" &&
  has(event.severity) &&
  event.severity >= 4
```

Identifier-like JSON keys support `event.key`; use bracket access for keys
containing dots, spaces, reserved words, or other punctuation, for example
`event["process.name"] == "loginwindow"`. CEL collection macros are
available, so a list can be tested with an expression such as
`event.tags.exists(tag, tag == "admin")`. JSON integers become CEL `int` or
`uint`, decimal and exponent values become `double`, and comparisons across
numeric types are enabled.

The expression is compiled during configuration validation and must have a
boolean result. Evaluation errors—including an unguarded missing field, an
incompatible type, or an out-of-range number—fail the run before any finding is
published. Use `has(event.field)` when a key is optional. Venator limits the
expression to 4,096 code points, parser depth to 100, and runtime cost to
100,000 units per event. Expressions are stateless and cannot transform
records, aggregate a batch, correlate events, or maintain a time window.

Expression evaluation runs after source size checks and before exclusions. Run
reports use `queried` for source records, `matched` for events whose expression
returned true (or all events when `expr` is omitted), `excluded` for matched
events suppressed by exclusions, and `findings` for the final count.

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

Identity is evaluated after expression evaluation and exclusions. Different
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

Reviewer credentials and model selection live in the global configuration. For
example, the bundled [local review rule](../config/examples/llm-review-rule.yaml)
can be paired with:

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

An exclusion file is a YAML list. Each entry contains exactly one non-empty
`and` or `or` condition list; groups are not nested. A record is excluded when
any entry matches.

```yaml
- conditions:
    and:
      - field: username
        operator: equals
        value: test
      - field: source_ip
        operator: in
        values: [192.0.2.10, 192.0.2.11]
```

| Operator | Accepted operand | Match |
| --- | --- | --- |
| `equals` | `value` (explicit empty string allowed) | Field equals `value`. |
| `not_equals` | `value` (explicit empty string allowed) | Field differs from `value`. |
| `contains` | Non-empty `value` | Field contains `value`. |
| `regex` | Non-empty `value` containing a valid Go regular expression | Expression matches the field. |
| `in` | Non-empty `values` list | Field equals an entry. |
| `not_in` | Non-empty `values` list | Field differs from every entry. |

Every condition requires a non-empty `field`, which names an exact top-level
source-record key; dots have no path-traversal meaning. Scalar operators reject
`values`, and set operators reject `value`; this prevents ambiguous or
accidentally broad exclusions. A missing or null field, or a value that cannot
be represented as text, does not match, including for negative operators.

Local and non-Helm deployments may use absolute paths or paths relative to the
rule file; the target must be a regular file, not a pipe or device. In the
chart, put exclusion files directly under `config/exclusions/` and use exactly
`/app/exclusion/<name>.yaml`; Helm rejects relative, nested, or unpackaged
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
