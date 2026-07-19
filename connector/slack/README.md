# Slack connector

Slack is a human-notification sink, not a finding archive. It displays canonical
finding JSON as plain attachment text with Markdown disabled, preventing log
content from creating mentions or links. Webhooks require HTTPS except for
loopback integration tests, redirects are disabled, response diagnostics are
sanitized, and transport errors never include the credential-bearing URL.

`maxFindings` defaults to 20 and may be set from 1 to 50. The complete webhook
payload is capped at 512 KiB. A batch above either limit fails before network
delivery; route high-volume rules to ClickHouse, OpenSearch, BigQuery, or
Pub/Sub and send Slack only an intentionally bounded alert stream.

```yaml
slack:
  instances:
    alerts:
      webhookURL: ${SLACK_WEBHOOK_URL}
      maxFindings: 20
```
