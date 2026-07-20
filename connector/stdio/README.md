# NDJSON file, stdin, and stdout connectors

`stdin.default` reads newline-delimited JSON objects from standard input;
`ndjson.<instance>` reads the finite regular file named by that instance's
global `path`. Empty lines are ignored, and each non-empty line must contain
exactly one JSON object. `stdout.default` writes one complete canonical finding
per line. Operational logs remain on stderr, so stdout can be piped into
another program.

Configure a finite file in global YAML, then refer to it from a rule:

```yaml
ndjson:
  instances:
    scanner-output:
      path: ./findings.ndjson
```

```yaml
source: ndjson.scanner-output
language: CEL
query: "true"
```

A relative `path` resolves from the global YAML. For both local source forms,
the rule's required CEL `query` evaluates the current JSON object as `event`.
Matching events continue to exclusions and finding construction. Use the
quoted string `"true"` when every input object is already a candidate. Parsing,
transforms, correlation, and windows remain upstream. See the
[rule reference](../../docs/rule-reference.md#local-ndjson-query).

Every input record is limited to 4 MiB, excluding its newline delimiter. This
per-record safety limit is fixed and independent of `runtime.maxBytes`, which
bounds the aggregate input and materialized output of a run.
`runtime.maxRecords` bounds the number of decoded objects. Limit failures name
the source (`stdin.default` or `ndjson.<instance>`) so scheduler diagnostics
remain actionable.

`stdin.default` is a finite pipe source, not a live subscription: it reads until
EOF, then Venator builds and publishes findings. Do not pipe `tail -F` into it:
an unclosed pipe reaches a row, byte, or time limit without becoming a live
detector. Use it with a bounded producer command whose exit status the caller
preserves.

An NDJSON instance accepts only a regular file; point it at a completed event
or candidate batch. It reads from the beginning on every invocation and does
not expand globs, tail, checkpoint, or wait for appended data. For continuously
growing logs, use a collector with explicit rotation, offset, buffering, and
retry semantics.
