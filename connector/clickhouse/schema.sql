CREATE DATABASE IF NOT EXISTS venator;

CREATE TABLE IF NOT EXISTS venator.findings
(
    detected_at DateTime64(6, 'UTC'),
    event_at Nullable(DateTime64(6, 'UTC')),
    run_id String,
    finding_id FixedString(64),
    rule_id String,
    rule_name String,
    rule_status LowCardinality(String),
    confidence LowCardinality(String),
    source LowCardinality(String),
    output_format LowCardinality(String),
    actor_user_name String,
    actor_user_uid String,
    resource_name String,
    resource_type LowCardinality(String),
    resource_uid String,
    src_hostname String,
    src_ip String,
    dst_hostname String,
    dst_ip String,
    message String,
    event_id String,
    event_index String,
    tags Array(String),
    ttp_ids Array(String),
    payload String CODEC(ZSTD(3)),
    INDEX detected_at_minmax detected_at TYPE minmax GRANULARITY 1
)
ENGINE = ReplacingMergeTree(detected_at)
-- Stable hash partitions let retries replace the same finding even when two
-- attempts cross a calendar-month boundary. Time-based retention can use TTL.
PARTITION BY cityHash64(finding_id) % 32
ORDER BY (rule_id, finding_id);
