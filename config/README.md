# Venator Helm chart

Helm remains Venator's first-class rule-fleet deployment. The chart discovers
`rules/**/*.yaml`, creates one ConfigMap for every rule, and creates one
independent Kubernetes CronJob for every enabled rule. This preserves per-rule
Job history, logs, retries, manual reruns, and debugging in GKE or another
Kubernetes control plane.

```sh
helm lint config --strict
helm template venator config --namespace venator
helm upgrade --install venator config \
  --namespace venator --create-namespace
```

Rule names used by Helm must be unique DNS-1123 labels of at most 52 characters;
`global` is reserved for the chart's global ConfigMap.
Completed Jobs are not TTL-deleted by default; `successfulJobsHistoryLimit`
and `failedJobsHistoryLimit` control retention. Set
`cronJob.ttlSecondsAfterFinished` only when deliberate early deletion is more
important than cluster-side debugging history.

The global `secretEnv` list remains backward compatible. For least privilege,
set it to `[]` and key `ruleSecretEnv` or `ruleEnvFrom` by rule name. The
`serviceAccount` block can create a KSA, including the GKE Workload Identity GSA
annotation; `ruleServiceAccounts` selects narrower existing KSAs per rule.

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
below that mount path.

The bundled example rules are disabled. Add or enable a real source-backed rule
before installation; the chart intentionally does not schedule an empty-stdin
smoke job.
