# Linux systemd timer

The templated unit maps an instance name to
`/etc/venator/rules/<instance>.yaml`. Install the binary and configuration,
then enable one timer per rule:

```sh
sudo install -m 0755 bin/venator /usr/local/bin/venator
sudo install -d -m 0755 /etc/venator/rules
sudo install -m 0644 deploy/examples/global.yaml /etc/venator/global.yaml
sudo install -m 0644 deploy/examples/rule.yaml \
  /etc/venator/rules/example.yaml
sudo install -m 0644 deploy/examples/events.ndjson \
  /etc/venator/rules/events.ndjson
sudo install -m 0644 deploy/systemd/venator@.service \
  deploy/systemd/venator@.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now venator@example.timer
```

Test the one-shot service and inspect logs:

```sh
sudo systemctl start venator@example.service
systemctl status venator@example.service
journalctl -u venator@example.service
systemctl list-timers 'venator@*'
```

The default timer runs hourly with a small randomized delay and catches up once
after downtime. Override the schedule for a rule without editing the shipped
unit:

```ini
# /etc/systemd/system/venator@example.timer.d/schedule.conf
[Timer]
OnCalendar=
OnCalendar=*-*-* 02:15:00
RandomizedDelaySec=5min
```

Run `systemctl daemon-reload` after adding the drop-in. Put `${VARIABLE}` values
used by global YAML in `/etc/venator/venator.env`, mode `0600`; systemd reads it
before the dynamically allocated service user starts. The hardening profile
allows outbound network access and read-only home access. If a local source
needs a protected directory, grant the narrow path with a unit drop-in instead
of disabling all sandboxing.

The sample is a real finite-file run, not an empty-stdin placeholder. It reads
`events.ndjson` next to the rule and selects one event with CEL. Replace both
files with a production source; for an actively growing log, use an external
checkpointing collector to manage offsets and rotation instead of rereading an
unbounded file on every timer.
