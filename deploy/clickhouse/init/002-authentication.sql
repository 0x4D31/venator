CREATE DATABASE IF NOT EXISTS security;

CREATE TABLE IF NOT EXISTS security.authentication
(
    event_id String,
    timestamp DateTime64(6, 'UTC'),
    username String,
    source_ip String,
    outcome LowCardinality(String)
)
ENGINE = ReplacingMergeTree
ORDER BY event_id;
