# NDJSON stdin, file, and stdout connectors

`stdin.default` reads newline-delimited JSON objects from standard input;
`file.ndjson` reads the finite regular file named by `rule.query`. Empty lines
are ignored, and each non-empty line must contain exactly one JSON object.
`stdout.default` writes one complete canonical finding per line. Operational
logs remain on stderr, so stdout can be piped into another program.

Every input record is limited to 4 MiB, excluding its newline delimiter. This
per-record safety limit is fixed and independent of `runtime.maxBytes`, which
bounds the aggregate input and materialized output of a run.
`runtime.maxRecords` bounds the number of decoded objects. Limit failures name
the source (`stdin.default` or `file.ndjson`) so scheduler diagnostics remain
actionable.

Use `stdin.default` for streams and pipes. `file.ndjson` deliberately accepts
only regular files and does not tail, checkpoint, or wait for appended data; a
scheduler or agent should invoke Venator again for each finite snapshot.
