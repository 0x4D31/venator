# Venator Helm chart

Helm remains Venator's first-class rule-fleet deployment. The chart discovers
`rules/**/*.yaml`, creates one ConfigMap for every rule, and creates one
independent Kubernetes CronJob for every enabled rule. This preserves per-rule
Job history, logs, retries, manual reruns, and debugging in GKE or another
Kubernetes control plane. Kubernetes 1.27 or newer is required because the
CronJobs set `spec.timeZone`.

```sh
helm lint config --strict
helm template venator config --namespace venator --kube-version 1.27.0
helm upgrade --install venator config \
  --namespace venator --create-namespace
```

Rule names used by Helm must be unique DNS-1123 labels of at most 52 characters;
`global` is reserved for the chart's global ConfigMap. Every rule UID must be a
non-empty string, and enabled rules may not share a UID. An enabled rule must
also have a non-empty string `schedule`; the chart passes that opaque value to
Kubernetes for CronJob syntax validation.

Completed Jobs are not TTL-deleted by default; `successfulJobsHistoryLimit`
and `failedJobsHistoryLimit` control retention. Set
`cronJob.ttlSecondsAfterFinished` only when deliberate early deletion is more
important than cluster-side debugging history.

`secretEnv` defaults to an empty list. Add shared credentials there only when
every rule needs them; otherwise key `ruleSecretEnv` or `ruleEnvFrom` by rule
name. The `serviceAccount` block can create a KSA, including the GKE Workload
Identity GSA annotation; `ruleServiceAccounts` selects narrower existing KSAs
per rule. All rule-scoped maps reject keys that do not exactly match a packaged
rule name. Environment variable names must be unique across the secret and
direct entries applied to each rule.

ClickHouse CA and mTLS files can be mounted without editing the chart. Use
`extraVolumes` plus `extraVolumeMounts` for every rule, or key
`ruleExtraVolumes` and `ruleExtraVolumeMounts` by rule name for isolation. For
example:

```yaml
ruleExtraVolumes:
  repeated-authentication-failures:
    - name: clickhouse-tls
      secret:
        secretName: clickhouse-client-tls
ruleExtraVolumeMounts:
  repeated-authentication-failures:
    - name: clickhouse-tls
      mountPath: /var/run/secrets/venator/clickhouse
      readOnly: true
```

The corresponding connector can reference `caFile`, `certFile`, and `keyFile`
below that mount path. Extra volume names and mount paths must be unique for a
rule; `rule-volume`, `config-volume`, `exclusion-volume`, and `tmp` are reserved
names. Extra mounts cannot overlap `/app/rule`, `/app/config`, or
`/app/exclusion`, cannot replace `/tmp`, and cannot be an ancestor of those
paths. A mount below `/tmp`, such as `/tmp/cache`, is allowed. Every extra mount
must have a matching extra volume.

Put exclusion YAML directly under `exclusions/`. A rule that uses it must set
`exclusionsPath` to exactly `/app/exclusion/<name>.yaml`; relative, nested, and
unpackaged paths are rejected during rendering.

The bundled example rule is disabled. Add or enable a real source-backed rule
before installation; the chart intentionally does not schedule an empty-stdin
smoke job. Rule and exclusion syntax is documented in the
[v0.2.0 reference](https://github.com/0x4D31/venator/blob/v0.2.0/docs/rule-reference.md).
