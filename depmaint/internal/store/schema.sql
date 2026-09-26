CREATE TABLE IF NOT EXISTS snapshot (
  id         VARCHAR(64) PRIMARY KEY,
  root       TEXT NOT NULL,
  created_at VARCHAR(32) NOT NULL,
  expander   TEXT NOT NULL,
  node_count INT NOT NULL,
  edge_count INT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_snapshot_root_created ON snapshot (root, created_at);

CREATE TABLE IF NOT EXISTS node (
  snapshot_id VARCHAR(64) NOT NULL,
  key         TEXT NOT NULL,
  kind        VARCHAR(16) NOT NULL,
  direct      BOOLEAN NOT NULL,
  PRIMARY KEY (snapshot_id, key)
);

CREATE TABLE IF NOT EXISTS edge (
  snapshot_id VARCHAR(64) NOT NULL,
  from_key    TEXT NOT NULL,
  to_key      TEXT NOT NULL,
  requirement TEXT NOT NULL,
  resolved    TEXT NOT NULL,
  internal    BOOLEAN NOT NULL,
  PRIMARY KEY (snapshot_id, from_key, to_key, requirement)
);

CREATE TABLE IF NOT EXISTS enrichment (
  source     VARCHAR(64) NOT NULL,
  key        TEXT NOT NULL,
  payload    TEXT NOT NULL,
  fetched_at VARCHAR(32) NOT NULL,
  PRIMARY KEY (source, key)
);
