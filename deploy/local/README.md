# Local binary

Local execution is the smallest Venator deployment and is a useful default for
workstations, home labs, and agent-driven workflows.

## Build and install

Venator v0.2.0 requires Go 1.25.12 or newer.

```sh
mkdir -p bin
go build -trimpath -o bin/venator .
install -d -m 0755 "$HOME/.local/bin"
install -m 0755 bin/venator "$HOME/.local/bin/venator"
```

Put configuration below a user-owned directory and restrict it before adding
credentials:

```sh
install -d -m 0700 "$HOME/.config/venator/rules"
install -m 0600 deploy/examples/global.yaml \
  "$HOME/.config/venator/global.yaml"
install -m 0600 deploy/examples/rule.yaml \
  "$HOME/.config/venator/rules/example.yaml"
install -m 0600 deploy/examples/events.ndjson \
  "$HOME/.config/venator/rules/events.ndjson"
```

Run the rule immediately:

```sh
"$HOME/.local/bin/venator" \
  run \
  --global-config "$HOME/.config/venator/global.yaml" \
  --rule-config "$HOME/.config/venator/rules/example.yaml"
```

The process logs to standard error. Let the caller capture that stream and
preserve the exit status. Use [`../launchd/`](../launchd/) on macOS or
[`../systemd/`](../systemd/) on Linux when the operating system should own the
schedule and retries.

The sample exercises the finite `file.ndjson` source. It intentionally rereads
the snapshot on each invocation; stable finding IDs help idempotent sinks, but
an actively tailed file needs a collector or wrapper with explicit offset and
retry checkpointing. The included Tenzir example watches for newly created
spool files rather than checkpointing one continuously growing file.

## Credentials

The global YAML can reference environment variables such as
`${OPENSEARCH_PASSWORD}`. Supply them with the host's secret manager. If a local
environment file is unavoidable, keep it outside the repository, set mode
`0600`, and source it only from a launcher you control. Environment files are
ignored by Git.
