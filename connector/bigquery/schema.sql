-- Replace YOUR_PROJECT before applying this schema.
CREATE SCHEMA IF NOT EXISTS `YOUR_PROJECT.venator`;

CREATE TABLE IF NOT EXISTS `YOUR_PROJECT.venator.findings`
(
  detected_at TIMESTAMP NOT NULL,
  event_at TIMESTAMP,
  run_id STRING NOT NULL,
  finding_id STRING NOT NULL,
  rule_id STRING NOT NULL,
  rule_name STRING,
  confidence STRING,
  source STRING,
  payload STRING NOT NULL
)
PARTITION BY DATE(detected_at)
CLUSTER BY rule_id, finding_id;
