# OpenSearch connector

OpenSearch supports SQL and PPL sources plus an idempotent canonical-finding
sink. SQL requests set `fetch_size: 0` by default so aggregation and join
queries, which OpenSearch cannot cursor-page, remain valid. Venator validates
the response shape, byte limit, and actual row count. It does not equate
OpenSearch's `total` (matching documents) with returned rows because bounded
SQL and aggregate queries legitimately differ. Give nonpaged rules an explicit
query bound and test that bound against the target cluster.

For a basic SQL query whose complete result does not fit in one response, opt in
to documented cursor pagination on that connector instance:

```yaml
opensearch:
  instances:
    paged-logs:
      url: https://opensearch.example:9200
      sqlFetchSize: 1000
```

`0` disables pagination. When enabled, `sqlFetchSize` must be positive and no
greater than `runtime.maxRecords`. Do not enable it for aggregation or join
queries. Create a second instance pointing to the same cluster when some rules
need cursor pagination and others use complex SQL. PPL does not expose the same
documented pagination and cursor-close contract; its returned result is
therefore treated as the query result and should also be explicitly bounded.

The sink defaults to `venator-findings-v1`; override it with `index` on the
connector instance. Index names are restricted to lowercase letters, digits,
dashes, underscores, and dots, and must begin with a letter or digit.

Install [`index-template.json`](index-template.json) before the first write:

```sh
curl --fail --user "$OPENSEARCH_USERNAME" \
  --header 'Content-Type: application/json' \
  --request PUT "$OPENSEARCH_URL/_index_template/venator-findings-v1" \
  --data-binary @connector/opensearch/index-template.json
```

`curl` prompts for the password so it is not exposed in the process list. For
automation, use a permission-restricted curl config or another secret-aware HTTP
client.

The template indexes stable envelope and signal fields while storing `payload`
with mapping disabled. This preserves heterogeneous payload shapes without
dynamic mapping conflicts or unbounded field growth. If `index` uses another
prefix, copy the template and update `index_patterns` accordingly.

For local evaluation, set `OPENSEARCH_INITIAL_ADMIN_PASSWORD` to a strong test
password and start `docker-compose.yml`. OpenSearch listens only on loopback;
Dashboards is available at `http://localhost:5601` with user `admin` and that
password. The stack uses OpenSearch's demo security configuration and is not a
production deployment.
