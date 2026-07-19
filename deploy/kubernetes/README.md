# Plain Kubernetes CronJob

This Kubernetes 1.27+ deployment is a scheduler-neutral alternative to the Helm chart. The
checked-in Kustomization generates a global ConfigMap and a rule ConfigMap
containing a finite NDJSON smoke event. It optionally imports every key from
the `venator-secrets` Secret as an environment variable.

Render or apply the runnable sample directly:

```sh
kubectl kustomize deploy/kubernetes
kubectl apply -k deploy/kubernetes
```

For a production rule, create or update ConfigMaps and a Secret from your own
configuration instead:

```sh
NAMESPACE=venator \
GLOBAL_CONFIG=deploy/kubernetes/global.yaml \
RULE_CONFIG=deploy/kubernetes/rule.yaml \
RULE_DATA_FILE=deploy/kubernetes/events.ndjson \
  sh scripts/create_configmap.sh

cp scripts/dot_vcfg.env .vcfg.env
chmod 0600 .vcfg.env
# Replace every example value, then:
NAMESPACE=venator ENV_FILE=.vcfg.env sh scripts/create_secret.sh
```

Create a manual Job and inspect it in the same way as a scheduled run:

```sh
kubectl create job --from=cronjob/venator-example \
  "venator-manual-$(date +%s)" -n venator
kubectl logs -n venator -l job-name="$(kubectl get jobs -n venator \
  --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')"
```

Make one named CronJob and rule ConfigMap per rule. If a rule uses an exclusion
file, create another ConfigMap, mount only that ConfigMap, and make
`exclusionsPath` match the mounted file. The sample intentionally has no
unconditional exclusion volume, so a rule without exclusions can start.
For a database-backed rule, set `RULE_DATA_FILE=` when invoking the helper; for
another finite file, set both `RULE_DATA_FILE` and its optional
`RULE_DATA_KEY` to match `rule.query`.

The helper defaults to the sample names referenced by `cronjob.yaml`. For a
fleet, set a unique `RULE_CONFIGMAP_NAME` and, when applicable, a unique
`EXCLUSIONS_CONFIGMAP_NAME` on every invocation, then use those exact names in
that rule's CronJob volumes. `GLOBAL_CONFIGMAP_NAME` can remain shared:

```sh
RULE_CONFIGMAP_NAME=venator-repeated-auth-rule \
EXCLUSIONS_CONFIGMAP_NAME=venator-repeated-auth-exclusions \
RULE_CONFIG=/path/to/repeated-auth.yaml \
EXCLUSIONS_CONFIG=/path/to/repeated-auth-exclusions.yaml \
RULE_DATA_FILE= \
  sh scripts/create_configmap.sh
```

The manifest forbids overlap, bounds late starts and run duration, disables the
service-account token, runs as a non-root user on a read-only filesystem, and
keeps Job history without a TTL deleting it early. Annotate the `venator`
ServiceAccount for GKE Workload Identity when BigQuery or Pub/Sub uses ADC.
Kubernetes CronJobs can occasionally create zero or
multiple Jobs; query windows and publishers still need deterministic
deduplication.
The minimum is Kubernetes 1.27 because the CronJob declares `spec.timeZone`.

For a repository-managed rule fleet, use the first-class chart in
[`config/`](../../config/). It packages every enabled rule as an independent
CronJob—the workflow that makes rule status and debugging convenient in GKE.
These plain Kustomize resources remain useful for operators who prefer to own
their manifests outside Helm.
