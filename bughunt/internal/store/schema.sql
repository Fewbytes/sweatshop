CREATE TABLE IF NOT EXISTS run (
  id                     VARCHAR(64) PRIMARY KEY,
  started_at             VARCHAR(32) NOT NULL,
  commit_sha             VARCHAR(64) NOT NULL,
  mode                   VARCHAR(16) NOT NULL,
  diff_base              VARCHAR(64) NOT NULL,
  detectors_used         TEXT NOT NULL,
  detectors_unavailable  TEXT NOT NULL,
  agentsh_invocation_ids TEXT NOT NULL,
  finding_count          INT NOT NULL,
  new_count              INT NOT NULL
);

CREATE TABLE IF NOT EXISTS finding (
  fingerprint    VARCHAR(64) PRIMARY KEY,
  rule_id        VARCHAR(255) NOT NULL,
  detector       VARCHAR(64) NOT NULL,
  file           TEXT NOT NULL,
  line           INT NOT NULL,
  symbol         TEXT NOT NULL,
  message        TEXT NOT NULL,
  severity       VARCHAR(16) NOT NULL,
  confidence     DOUBLE NOT NULL,
  status         VARCHAR(16) NOT NULL,
  first_seen_run VARCHAR(64) NOT NULL,
  last_seen_run  VARCHAR(64) NOT NULL,
  seen_count     INT NOT NULL,
  test_path      TEXT NOT NULL,
  test_kept      BOOLEAN NOT NULL,
  fix_commit     VARCHAR(64) NOT NULL,
  branch         TEXT NOT NULL,
  pr_url         TEXT NOT NULL,
  bead_id        VARCHAR(64) NOT NULL,
  triage_note    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rule (
  id                VARCHAR(255) PRIMARY KEY,
  engine            VARCHAR(32) NOT NULL,
  path              TEXT NOT NULL,
  origin_finding    VARCHAR(64) NOT NULL,
  created_at        VARCHAR(32) NOT NULL,
  hit_count         INT NOT NULL,
  tp_count          INT NOT NULL,
  fp_count          INT NOT NULL,
  status            VARCHAR(16) NOT NULL,
  retirement_reason TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS lesson (
  class      VARCHAR(255) PRIMARY KEY,
  doc_path   TEXT NOT NULL,
  rule_ids   TEXT NOT NULL,
  updated_at VARCHAR(32) NOT NULL
);
