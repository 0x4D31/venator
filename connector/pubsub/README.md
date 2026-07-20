# Google Cloud Pub/Sub sink

The Pub/Sub connector publishes one canonical `venator.finding/v1` JSON
message per finding. It is a sink only.

```yaml
pubsub:
  instances:
    alerts:
      projectID: my-project
      topicID: venator-findings
```

Reference the instance as `pubsub.alerts` in `publishers` or
`bestEffortPublishers`. Authentication uses Google Application Default
Credentials, including Workload Identity on GKE.

Each message carries `schema_version`, `finding_id`, `run_id`, and `rule_id`
attributes. Venator JSON-encodes and checks every message against Pub/Sub's
10,000,000-byte request limit before publishing any finding in the batch, then
waits for every publish result. Pub/Sub delivery is at least once; consumers
should use the stable `finding_id` as their idempotency key.
