# Local binary

Local execution is the smallest Venator deployment and is a useful default for
workstations, home labs, and agent-driven workflows.

## Install a release binary

Download the asset for the host from the v0.2.0 GitHub release, verify it
against `SHA256SUMS`, and install it in a user-owned binary directory. For
example, on Apple Silicon:

```sh
curl -fLO https://github.com/0x4D31/venator/releases/download/v0.2.0/venator-darwin-arm64
curl -fLO https://github.com/0x4D31/venator/releases/download/v0.2.0/SHA256SUMS
grep ' venator-darwin-arm64$' SHA256SUMS | shasum -a 256 -c -
install -d -m 0755 "$HOME/.local/bin"
install -m 0755 venator-darwin-arm64 "$HOME/.local/bin/venator"
```

Release assets are available for Linux and macOS on amd64 and arm64. Replace
the asset name above for the host; Linux users can use `sha256sum -c`.

## Build from source

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

The sample exercises the finite `file.ndjson` source. It rereads the snapshot
on every invocation. Stable finding IDs help idempotent sinks, but the source
does not filter arbitrary raw logs or own a read offset.

For continuously growing files or journald, use a checkpointing collector and
hand Venator completed candidate batches. Do not use `tail -F | venator`:
publication waits for EOF, and a row, byte, or time limit may be reached first.
The [lightweight local-detection guide](../../docs/local-detection.md) covers
durable spool handoff, collector boundaries, and 64-bit Raspberry Pi
deployments.

## Credentials

The global YAML can reference environment variables such as
`${OPENSEARCH_PASSWORD}`. Supply them with the host's secret manager. If a local
environment file is unavoidable, keep it outside the repository, set mode
`0600`, and source it only from a launcher you control. Environment files are
ignored by Git.
