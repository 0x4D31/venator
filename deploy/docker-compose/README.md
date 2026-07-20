# Docker Compose

The Compose service is an ephemeral, one-rule job rather than a daemon.

From this directory, validate and run it with:

```sh
docker compose config
docker compose --profile run run --rm -T venator < ../../config/examples/events.ndjson
```

By default it mounts the repository's `config/` directory read-only and forces
the intentionally disabled example rule for this explicit smoke run. Set
`VENATOR_CONFIG_DIR` to a directory with the same `files/` and
`rules/` layout, or edit the two command paths for your layout. Override the
rule without changing the file:

```sh
docker compose --profile run run --rm venator \
  run \
  --global-config /etc/venator/files/global_config.yaml \
  --rule-config /etc/venator/rules/my-rule.yaml
```

Compose does not provide a dependable recurring scheduler. Invoke the command
from a systemd timer, cron, a home-automation system, or an AI agent. Preserve
the container exit status so source and publisher failures remain visible.

Copy `.env.example` to `.env` only on the deployment host, set mode `0600`, and
replace its values. Do not commit `.env`; it is ignored by Git and Docker build
context rules. The Compose file explicitly passes the documented connector
variables into the one-shot container; Compose's `.env` file alone is otherwise
only an interpolation source. Add another list-form environment entry when a
custom configuration references a different variable. A production secret
manager is preferable.
