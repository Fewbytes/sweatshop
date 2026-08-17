# agentsh Analytics — Parquet Exporter & DuckDB Queries

`agentsh-analytics` is a standalone, offline analytics exporter and querying tool. It exports `agentshd` SQLite invocation metadata, Drain-clustered log templates, structured test results, and failure frames to Apache Parquet format.

## Architecture

```text
                    ┌────────────────────────┐
                    │ agentshd runtime       │ (Pure Go, 0 DuckDB/cgo deps)
                    │  - history.db (SQLite) │
                    │  - blobs/ (Raw logs)   │
                    └───────────┬────────────┘
                                │
                    ┌───────────▼────────────┐
                    │ agentsh-analytics      │ (Standalone CLI / pure Go exporter)
                    │  export --workspace .  │
                    └───────────┬────────────┘
                                │
       ┌────────────────────────┼────────────────────────┬────────────────────────┐
       │                        │                        │                        │
┌──────▼──────────────┐  ┌──────▼──────────────┐  ┌──────▼──────────────┐  ┌──────▼──────────────┐
│ invocations.parquet │  │ templates.parquet   │  │ errors.parquet      │  │ test_results.parquet│
└─────────────────────┘  └─────────────────────┘  └─────────────────────┘  └─────────────────────┘
                                │
                    ┌───────────▼────────────┐
                    │ DuckDB CLI / SQL       │ (Optional analytics engine)
                    └────────────────────────┘
```

- **Runtime Isolation**: `agentshd` runtime has zero link-time or runtime dependencies on DuckDB or Parquet.
- **Pure-Go Parquet Generation**: The exporter produces standard Parquet files using pure Go (`parquet-go`), preserving single-binary static compilation without cgo.
- **Safe Blob Handling**: Missing or expired blobs are handled gracefully during export without aborting or corrupting metadata.

---

## Parquet Table Schemas

### 1. `invocations.parquet`

Primary invocation lifecycle metadata and execution status.

| Column | Type | Description |
| --- | --- | --- |
| `id` | `VARCHAR` | Unique invocation ID (e.g. `inv_1a2b3c4d`) |
| `session` | `VARCHAR` | Session name (default: `default`) |
| `command` | `VARCHAR` | Raw command string executed |
| `cwd` | `VARCHAR` | Working directory at invocation start |
| `state` | `VARCHAR` | State: `exited`, `timeout`, `killed`, `waiting_on_input`, `daemon_lost` |
| `exit_code` | `INTEGER` | Process exit status code (`0` for success) |
| `reason` | `VARCHAR` | Reason: `ok`, `nonzero`, `timeout`, `killed`, `oom`, `not_found`, etc. |
| `started_at_unix_ms` | `BIGINT` | Invocation start timestamp (UNIX epoch milliseconds) |
| `ended_at_unix_ms` | `BIGINT` | Invocation end timestamp (UNIX epoch milliseconds) |
| `duration_ms` | `BIGINT` | Execution duration in milliseconds |
| `stdout_sha256` | `VARCHAR` | Content-addressed SHA256 digest of stdout blob |
| `stdout_bytes` | `BIGINT` | Total bytes in stdout stream |
| `stdout_lines` | `BIGINT` | Total lines in stdout stream |
| `stderr_sha256` | `VARCHAR` | Content-addressed SHA256 digest of stderr blob |
| `stderr_bytes` | `BIGINT` | Total bytes in stderr stream |
| `stderr_lines` | `BIGINT` | Total lines in stderr stream |
| `summary_type` | `VARCHAR` | Formatter family: `go test`, `pytest`, `jest`, `cargo`, `kubectl`, `terraform` |
| `summary_state` | `VARCHAR` | Structured state: `passed`, `failed`, `ok`, `changes_planned` |

### 2. `templates.parquet`

Drain-clustered log templates derived from stdout/stderr streams.

| Column | Type | Description |
| --- | --- | --- |
| `invocation_id` | `VARCHAR` | Foreign key referencing `invocations.id` |
| `stream` | `VARCHAR` | Stream: `stdout` or `stderr` |
| `template_id` | `VARCHAR` | Deterministic 16-char hex hash of template |
| `template` | `VARCHAR` | Masked template string (with `<IP>`, `<NUM>`, `<UUID>`, etc.) |
| `count` | `BIGINT` | Occurrences of this template in the stream |
| `first_line` | `INTEGER` | 1-based line number of first occurrence |
| `last_line` | `INTEGER` | 1-based line number of last occurrence |
| `exemplar_offset` | `BIGINT` | Byte offset of exemplar line in raw blob |
| `exemplar` | `VARCHAR` | Representative raw unmasked line or stack trace |
| `level` | `VARCHAR` | Detected log level: `DEBUG`, `INFO`, `WARN`, `ERROR`, `FATAL` |
| `is_stack_trace` | `BOOLEAN` | True if template represents a collapsed multi-line stack trace |

### 3. `errors.parquet`

Extracted failure frames, assertion details, and error diagnostics.

| Column | Type | Description |
| --- | --- | --- |
| `invocation_id` | `VARCHAR` | Foreign key referencing `invocations.id` |
| `session` | `VARCHAR` | Session name |
| `command` | `VARCHAR` | Invocation command |
| `reason` | `VARCHAR` | Failure reason |
| `exit_code` | `INTEGER` | Process exit code |
| `timestamp_unix_ms` | `BIGINT` | Failure timestamp (UNIX epoch milliseconds) |
| `error_name` | `VARCHAR` | Name of failed test or error type |
| `error_message` | `VARCHAR` | Failure assertion or error description |
| `location` | `VARCHAR` | File and line location (e.g. `src/auth.test.ts:24`) |
| `excerpt` | `VARCHAR` | Relevant failure code excerpt / backtrace |

### 4. `test_results.parquet`

Structured test execution summaries per invocation.

| Column | Type | Description |
| --- | --- | --- |
| `invocation_id` | `VARCHAR` | Foreign key referencing `invocations.id` |
| `session` | `VARCHAR` | Session name |
| `command` | `VARCHAR` | Test command |
| `family` | `VARCHAR` | Runner: `go test`, `pytest`, `jest`, `cargo` |
| `status` | `VARCHAR` | Status: `passed`, `failed` |
| `passed` | `INTEGER` | Count of passing tests |
| `failed` | `INTEGER` | Count of failing tests |
| `skipped` | `INTEGER` | Count of skipped / ignored tests |
| `total` | `INTEGER` | Total tests executed |
| `duration` | `VARCHAR` | Reported test runner duration (e.g. `1.23s`) |
| `timestamp_unix_ms` | `BIGINT` | Test execution timestamp |

---

## Usage

### Exporting Parquet Files

```bash
# Export workspace history to default directory (.agentsh/analytics/)
agentsh-analytics export --workspace /path/to/workspace

# Export to a custom directory
agentsh-analytics export --workspace /path/to/workspace --out ./analytics_data
```

### Viewing Schemas & Example Queries

```bash
agentsh-analytics schema
```

### Querying with DuckDB

```bash
# Direct query through CLI wrapper
agentsh-analytics query --out ./analytics_data "SELECT count(*) FROM 'invocations.parquet'"

# Direct DuckDB CLI invocation against exported files
duckdb -c "
  SELECT summary_type, COUNT(*) as runs, SUM(CASE WHEN exit_code != 0 THEN 1 ELSE 0 END) as failures
  FROM 'analytics_data/invocations.parquet'
  GROUP BY summary_type;
"
```

---

## Example DuckDB Analytics Queries

### 1. Failure Rate and Execution Count by Command Family

```sql
SELECT summary_type,
       COUNT(*) as total_runs,
       SUM(CASE WHEN exit_code != 0 THEN 1 ELSE 0 END) as failed_runs,
       ROUND(SUM(CASE WHEN exit_code != 0 THEN 1.0 ELSE 0.0 END) / COUNT(*) * 100, 1) as failure_rate_pct,
       ROUND(AVG(duration_ms), 0) as avg_duration_ms
FROM 'invocations.parquet'
WHERE summary_type != ''
GROUP BY summary_type
ORDER BY total_runs DESC;
```

### 2. Top Failing Tests Across Invocations

```sql
SELECT error_name, location, error_message, COUNT(*) as occurrences
FROM 'errors.parquet'
WHERE error_name != ''
GROUP BY error_name, location, error_message
ORDER BY occurrences DESC
LIMIT 10;
```

### 3. Novel and Severe Log Templates Across Runs

```sql
SELECT t.template,
       t.level,
       SUM(t.count) as total_occurrences,
       COUNT(DISTINCT t.invocation_id) as affected_invocations,
       MIN(t.exemplar) as sample_log
FROM 'templates.parquet' t
WHERE t.is_stack_trace = true OR t.level IN ('ERROR', 'FATAL')
GROUP BY t.template, t.level
ORDER BY total_occurrences DESC
LIMIT 20;
```

### 4. Test Suite Pass Rates and Flakiness Trend

```sql
SELECT command,
       family,
       COUNT(*) as total_runs,
       SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed_runs,
       ROUND(AVG(passed), 1) as avg_passed,
       ROUND(AVG(failed), 1) as avg_failed,
       ROUND(AVG(total), 1) as avg_total
FROM 'test_results.parquet'
GROUP BY command, family
ORDER BY total_runs DESC;
```

### 5. Join Invocations with Derived Error Frames

```sql
SELECT i.id,
       i.command,
       i.state,
       i.exit_code,
       e.error_name,
       e.location,
       e.error_message
FROM 'invocations.parquet' i
JOIN 'errors.parquet' e ON i.id = e.invocation_id
ORDER BY i.started_at_unix_ms DESC
LIMIT 25;
```
