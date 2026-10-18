CREATE TABLE IF NOT EXISTS campaign (
  id           VARCHAR(32) PRIMARY KEY,
  type         VARCHAR(16) NOT NULL,
  package      TEXT NOT NULL,
  target       TEXT NOT NULL,
  roots        TEXT NOT NULL,
  status       VARCHAR(16) NOT NULL,
  max_attempts INT NOT NULL,
  max_seconds  INT NOT NULL,
  spent        INT NOT NULL,
  completed    INT NOT NULL,
  plan_json    TEXT NOT NULL,
  plan_hash    VARCHAR(64) NOT NULL,
  snapshot_id  VARCHAR(64) NOT NULL,
  created_at   VARCHAR(32) NOT NULL,
  updated_at   VARCHAR(32) NOT NULL
);

CREATE TABLE IF NOT EXISTS attempt (
  id             VARCHAR(48) PRIMARY KEY,
  campaign_id    VARCHAR(32) NOT NULL,
  step           INT NOT NULL,
  plan_hash      VARCHAR(64) NOT NULL,
  snapshot_before VARCHAR(64) NOT NULL,
  snapshot_after VARCHAR(64) NOT NULL,
  verdict        VARCHAR(16) NOT NULL,
  reason         TEXT NOT NULL,
  executor       TEXT NOT NULL,
  pr_url         TEXT NOT NULL,
  commit_sha     TEXT NOT NULL,
  created_at     VARCHAR(32) NOT NULL
);

CREATE TABLE IF NOT EXISTS escalation (
  id          VARCHAR(48) PRIMARY KEY,
  campaign_id VARCHAR(32) NOT NULL,
  reason      TEXT NOT NULL,
  bead_id     TEXT NOT NULL,
  created_at  VARCHAR(32) NOT NULL
);
