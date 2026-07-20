# Nomad periodic job

The example targets Nomad 1.8 or newer and is an hourly periodic batch job with
overlap prohibited. It requests a read-only Nomad host volume named
`venator-config`; it does not rely on the Docker driver's disabled-by-default
raw host-bind feature.

For the runnable finite-file sample, populate that directory first:

```sh
sudo install -d -m 0755 /opt/venator/config/files /opt/venator/config/rules
sudo install -m 0644 deploy/examples/global.yaml \
  /opt/venator/config/files/global_config.yaml
sudo install -m 0644 deploy/examples/rule.yaml \
  /opt/venator/config/rules/example.yaml
sudo install -m 0644 deploy/examples/events.ndjson \
  /opt/venator/config/files/events.ndjson
```

Register that path in each eligible Nomad client's agent configuration, then
restart the client:

```hcl
client {
  host_volume "venator-config" {
    path      = "/opt/venator/config"
    read_only = true
  }
}
```

The job's group `volume` and task `volume_mount` stanzas make the scheduling
requirement explicit and mount it at `/etc/venator`. See Nomad's
[host-volume configuration](https://developer.hashicorp.com/nomad/docs/configuration/client#host_volume-block)
and [job volume](https://developer.hashicorp.com/nomad/docs/job-specification/volume)
documentation. A dynamic host volume or artifact/template workflow can replace
the static host volume.

```sh
nomad job validate deploy/nomad/venator.nomad.hcl
nomad job plan deploy/nomad/venator.nomad.hcl
nomad job run deploy/nomad/venator.nomad.hcl
nomad job periodic force venator-example
```

Nomad's cron expression includes a seconds field. The sample
`0 5 * * * *` means five minutes past every hour. Duplicate the job for each
rule so the periodic expression, retries, resources, and failure status remain
independent.

Do not put connector secrets in the HCL or host bind mount. Add a `template`
stanza backed by Nomad Variables or Vault, set `env = true`, and reference those
environment variables from global YAML. Keep `prohibit_overlap = true` unless
every source window and publisher is idempotent.

The included global configuration gives Venator a five-minute runtime timeout;
keep that shorter than the periodic interval. Also alert on allocations that
never start or disappear, since a process timeout only covers a running task.
