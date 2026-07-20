# Generic webhook sink

The webhook connector sends one complete `venator.finding/v1` JSON object per
HTTP request. It is intended for automation receivers, durable queues, and
asynchronous investigation handoff; it is not an inbound Venator server.

```yaml
webhook:
  instances:
    agent:
      url: ${AGENT_WEBHOOK_URL}
      headers:
        Authorization: Bearer ${AGENT_WEBHOOK_TOKEN}
      signingSecret: ${AGENT_WEBHOOK_SECRET}
      timeout: 20s
      maxAttempts: 3
      maxFindings: 100
      maxPayloadBytes: 1048576
```

Reference the instance as `webhook.agent` in `publishers` when delivery is
required, or in `bestEffortPublishers` when it must not fail the run. Remote
endpoints require HTTPS; plain HTTP is accepted only for literal loopback IPs.
URLs cannot contain user information, a query, or a fragment. Put credentials
in environment-backed headers rather than the URL.

This is an egress boundary. Raw findings include the complete source record,
without automatic redaction. Use signal output or project fields upstream when
the receiver should not receive the full evidence record.

`signingSecret` is optional. When set, it follows the
[Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md)
HMAC-SHA256 scheme and must be `whsec_` plus base64 encoding of 24 to 64 random
bytes. Each attempt includes:

- `webhook-id`, derived from the run ID and finding ID and unchanged across
  in-process delivery attempts;
- `webhook-timestamp`, refreshed for each attempt;
- `webhook-signature` when signing is configured;
- `Idempotency-Key`, equal to `webhook-id`; and
- `Venator-Finding-ID`, the semantic finding ID, which remains stable across
  scheduler reruns when the configured evidence identity is unchanged.

The connector pre-encodes and validates every finding, including the configured
per-request `maxPayloadBytes` limit, before the first HTTP call; it rejects
rather than truncates invalid payloads. It does not follow redirects. Any 2xx
response is authoritative success. Connection errors, 408, 425, 429, and
retryable 5xx responses receive bounded in-process retries, with `Retry-After`
honored up to 30 seconds. Durable recovery is delegated to the scheduler and
can repeat a delivery that succeeded before a later request failed.

The receiver should verify signatures and timestamp freshness, deduplicate
`webhook-id`, durably enqueue the finding, and respond quickly. If repeated
scheduler runs should not start another investigation, deduplicate the separate
`Venator-Finding-ID`; unlike `webhook-id`, it intentionally spans canonical
bodies with different run metadata. Treat finding payloads as untrusted
evidence rather than agent instructions. The
[lightweight local-detection guide](../../docs/local-detection.md) covers safe
agent handoff and small-host deployment patterns.
