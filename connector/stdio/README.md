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

`stdin.default` is a finite pipe source, not a live subscription: it reads until
EOF, then Venator builds and publishes findings. Do not pipe `tail -F` into it:
an unclosed pipe reaches a row, byte, or time limit without becoming a live
detector. Use it with a bounded producer command whose exit status the caller
preserves.

`file.ndjson` accepts only regular files; point it at a completed snapshot. It
reads from the beginning on every invocation and does not tail, checkpoint, or
wait for appended data. For continuously growing logs, use a collector with
explicit rotation, offset, buffering, and retry semantics.
