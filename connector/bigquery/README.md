# BigQuery connector

A BigQuery instance is always a SQL source. Adding both `datasetID` and
`tableID` also enables the canonical finding sink. Venator applies
`maxBytesBilled` to every query (default 10 GiB), plus the global row and
encoded-result byte caps.

`language: SQL` describes the rule text; it is not a SQL sandbox. For a
source-only rule, grant the runtime identity only `roles/bigquery.jobUser` on
the project and `roles/bigquery.dataViewer` on the source dataset. Grant
`roles/bigquery.dataEditor` only on a destination dataset when the same process
uses a BigQuery sink. IAM, not SQL prefix inspection, is the authoritative
write boundary.

```yaml
bigquery:
  instances:
    security:
      projectID: my-project
      maxBytesBilled: 10737418240
      datasetID: venator
      tableID: findings
```

Apply [`schema.sql`](schema.sql) after replacing `YOUR_PROJECT`. Streaming
inserts use the stable finding ID as BigQuery's insert ID for best-effort retry
deduplication. BigQuery's deduplication window is not permanent, so downstream
queries should continue treating `finding_id` as the idempotency key.

For GKE, prefer Application Default Credentials through a least-privilege
Workload Identity binding over mounted service-account JSON.
