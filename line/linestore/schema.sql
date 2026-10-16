-- seq orders rows in the order they were saved, run_id joins test_result to run
-- and is empty on rows imported from before per-test results.
CREATE TABLE IF NOT EXISTS run (
  seq         INTEGER PRIMARY KEY,
  run_id      TEXT NOT NULL,
  commit_sha  TEXT NOT NULL,
  tree        TEXT NOT NULL,
  role        TEXT NOT NULL,
  agent_id    TEXT NOT NULL,
  runner      TEXT NOT NULL,
  started_at  TEXT NOT NULL,
  duration_ms INTEGER NOT NULL,
  checks      INTEGER NOT NULL,
  failures    INTEGER NOT NULL,
  passed      INTEGER NOT NULL,
  outcome     TEXT NOT NULL,
  report_key  TEXT NOT NULL,
  html_key    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS test_result (
  seq         INTEGER PRIMARY KEY,
  run_id      TEXT NOT NULL,
  commit_sha  TEXT NOT NULL,
  tree        TEXT NOT NULL,
  role        TEXT NOT NULL,
  started_at  TEXT NOT NULL,
  check_name  TEXT NOT NULL,
  package     TEXT NOT NULL,
  test        TEXT NOT NULL,
  outcome     TEXT NOT NULL,
  duration_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS kaizen_baseline (
  seq        INTEGER PRIMARY KEY,
  commit_sha TEXT NOT NULL UNIQUE,
  signals    TEXT NOT NULL
);

-- levels holds the one row of the latest published heijunka levels.
CREATE TABLE IF NOT EXISTS levels (
  id        INTEGER PRIMARY KEY CHECK (id = 1),
  agents    INTEGER NOT NULL,
  wip_limit INTEGER NOT NULL
);

-- imported names each legacy file already copied in (see Store.ImportLegacy).
CREATE TABLE IF NOT EXISTS imported (
  file TEXT PRIMARY KEY
);

-- mutation_run is one mutation-testing measurement of the files a kaizen run or
-- a landed card touched. score and sample_size are NULL, with the reason in
-- unavailable, when the tool could not finish: a missing score is not 0.
CREATE TABLE IF NOT EXISTS mutation_run (
  seq          INTEGER PRIMARY KEY,
  commit_sha   TEXT NOT NULL,
  source       TEXT NOT NULL,
  language     TEXT NOT NULL,
  scope        TEXT NOT NULL,
  tool         TEXT NOT NULL,
  tool_version TEXT NOT NULL,
  duration_ms  INTEGER NOT NULL,
  sample_size  INTEGER,
  score        REAL,
  unavailable  TEXT NOT NULL
);

-- line_event is the chronological record of line events: seq orders rows in the
-- order they happened, at is fixed-width UTC (see eventTimeFormat) so SQL can
-- compare it as text. card and station are empty when the event has none.
CREATE TABLE IF NOT EXISTS line_event (
  seq     INTEGER PRIMARY KEY,
  at      TEXT NOT NULL,
  kind    TEXT NOT NULL,
  card    TEXT NOT NULL,
  station TEXT NOT NULL,
  detail  TEXT NOT NULL
);
