# Slack connector

Slack is an intentionally lossy human-notification sink, not a finding archive.
Each `plain_text` Block Kit section contains a deterministic summary: rule and
finding IDs, source, event and detection times, review verdict, and either the
mapped message or a bounded payload preview. Using `plain_text` prevents log
content from creating mentions or links. Webhooks require HTTPS except for
loopback integration tests, redirects are disabled, response diagnostics are
sanitized, and errors never include the credential-bearing URL.

`maxFindings` defaults to 20 and may be set from 1 to Slack's 50-block message
limit. Summary values and evidence previews have smaller bounds, each complete
finding block is capped at 3,000 Unicode characters, and the webhook payload is
capped at 512 KiB. Route complete canonical envelopes to ClickHouse, OpenSearch,
BigQuery, or Pub/Sub and send Slack only an intentionally bounded alert stream.

```yaml
slack:
  instances:
    alerts:
      webhookURL: ${SLACK_WEBHOOK_URL}
      maxFindings: 20
```
