# AI-agent invocation

An agent can schedule or invoke Venator, but the agent should not rewrite the
rule, choose an ad-hoc time window, or reinterpret the process exit status.
Venator remains the deterministic detection and delivery boundary; the agent is
an optional scheduler, operator, and run-summary layer.

Give the agent an allow-listed executable plus fixed configuration paths. For
example, configure its process tool with this argument vector:

```text
/Users/alice/.local/bin/venator
run
--global-config
/Users/alice/.config/venator/global.yaml
--rule-config
/Users/alice/.config/venator/rules/example.yaml
```

The runnable sample in `deploy/examples/` uses `file.ndjson` and resolves
`events.ndjson` relative to the rule file. Install those three files
together, or have the agent provide NDJSON on stdin to a `stdin.default` rule
as one explicit pipeline whose producer status is also checked.

An appropriate recurring instruction is:

> Run the configured Venator command exactly once. Do not edit its YAML files or
> add shell operators. Preserve stdout, stderr, and the numeric exit status. A
> zero status is complete; a non-zero status is a failed run and must be
> reported without claiming that the detection succeeded.

Prefer an agent API that accepts an executable and argument array instead of a
shell command string. Give the agent read access to the binary and rule files,
but keep connector credentials in its secret store or inherited environment.
Use a native Venator publisher such as Slack for deterministic notification.
If the agent routes canonical stdout findings to Telegram or another service,
that routing step is a separate sink and must preserve failures; do not make the
agent parse log prose to decide whether a finding exists.

If the agent platform can overlap recurring runs, disable overlap or wrap the
job with the platform's concurrency control. Do not retry a successful run just
because the agent dislikes its natural-language summary.
