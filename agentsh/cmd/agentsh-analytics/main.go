package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/exporter"
	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/workspace"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "export":
		fs := flag.NewFlagSet("export", flag.ExitOnError)
		workspaceFlag := fs.String("workspace", "", "workspace directory")
		outFlag := fs.String("out", "", "output directory for parquet files")
		if err := fs.Parse(os.Args[2:]); err != nil {
			fatal(err)
		}

		paths, err := workspace.Resolve(*workspaceFlag)
		if err != nil {
			fatal(err)
		}

		stats, err := exporter.ExportWorkspace(context.Background(), paths, *outFlag)
		if err != nil {
			fatal(err)
		}

		encoded, _ := json.MarshalIndent(stats, "", "  ")
		fmt.Println(string(encoded))

	case "schema":
		printSchemasAndExamples()

	case "query":
		fs := flag.NewFlagSet("query", flag.ExitOnError)
		workspaceFlag := fs.String("workspace", "", "workspace directory")
		outFlag := fs.String("out", "", "output directory containing parquet files")
		if err := fs.Parse(os.Args[2:]); err != nil {
			fatal(err)
		}

		if fs.NArg() < 1 {
			fatal(fmt.Errorf("usage: agentsh-analytics query [--workspace DIR] [--out DIR] \"<SQL QUERY>\""))
		}
		query := fs.Arg(0)

		paths, err := workspace.Resolve(*workspaceFlag)
		if err != nil {
			fatal(err)
		}

		parquetDir := *outFlag
		if parquetDir == "" {
			parquetDir = filepath.Join(paths.StateDir, "analytics")
		}

		duckdbPath, err := exec.LookPath("duckdb")
		if err != nil {
			fmt.Printf("DuckDB is not found on PATH.\nTo run queries, install DuckDB (https://duckdb.org) and query the exported files directly in %s:\n\nExample:\n  duckdb -c %q\n", parquetDir, query)
			return
		}

		cmd := exec.Command(duckdbPath, "-c", query)
		cmd.Dir = parquetDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			fatal(err)
		}

	case "help", "--help", "-h":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`agentsh-analytics — standalone Parquet exporter and DuckDB analytics tool

Usage:
  agentsh-analytics export [--workspace DIR] [--out DIR]
    Export invocation metadata, log templates, errors, and test results to Parquet.

  agentsh-analytics query [--workspace DIR] [--out DIR] "<SQL>"
    Run a DuckDB SQL query against the exported Parquet tables.

  agentsh-analytics schema
    Display the documented Parquet schemas and example DuckDB SQL queries.`)
}

func printSchemasAndExamples() {
	fmt.Println(`# agentsh Analytics Parquet Schemas & Example DuckDB Queries

## 1. invocations.parquet
- id: string (primary key)
- session: string
- command: string
- cwd: string
- state: string ("exited", "timeout", "killed", "waiting_on_input")
- exit_code: int32
- reason: string ("ok", "nonzero", "timeout", "killed", "oom", etc.)
- started_at_unix_ms: int64
- ended_at_unix_ms: int64
- duration_ms: int64
- stdout_sha256: string
- stdout_bytes: int64
- stdout_lines: int64
- stderr_sha256: string
- stderr_bytes: int64
- stderr_lines: int64
- summary_type: string ("go test", "pytest", "jest", "cargo", "kubectl", "terraform")
- summary_state: string ("passed", "failed", "ok", "changes_planned")

## 2. templates.parquet
- invocation_id: string
- stream: string ("stdout", "stderr")
- template_id: string
- template: string (Drain-clustered template with <IP>, <NUM>, <UUID>, etc.)
- count: int64
- first_line: int32
- last_line: int32
- exemplar_offset: int64
- exemplar: string
- level: string ("DEBUG", "INFO", "WARN", "ERROR", "FATAL")
- is_stack_trace: bool

## 3. errors.parquet
- invocation_id: string
- session: string
- command: string
- reason: string
- exit_code: int32
- timestamp_unix_ms: int64
- error_name: string
- error_message: string
- location: string (file:line)
- excerpt: string

## 4. test_results.parquet
- invocation_id: string
- session: string
- command: string
- family: string ("go test", "pytest", "jest", "cargo")
- status: string ("passed", "failed")
- passed: int32
- failed: int32
- skipped: int32
- total: int32
- duration: string
- timestamp_unix_ms: int64

---
## Example DuckDB Queries

### Most frequent errors across invocations:
SELECT error_name, error_message, location, COUNT(*) as occurrences
FROM 'errors.parquet'
WHERE error_message != ''
GROUP BY error_name, error_message, location
ORDER BY occurrences DESC
LIMIT 10;

### Failure rate by command family:
SELECT summary_type,
       COUNT(*) as total_runs,
       SUM(CASE WHEN exit_code != 0 THEN 1 ELSE 0 END) as failed_runs,
       ROUND(SUM(CASE WHEN exit_code != 0 THEN 1.0 ELSE 0.0 END) / COUNT(*) * 100, 1) as failure_rate_pct
FROM 'invocations.parquet'
WHERE summary_type != ''
GROUP BY summary_type
ORDER BY total_runs DESC;

### Novel and rare error log templates:
SELECT template, level, SUM(count) as total_occurrences, COUNT(DISTINCT invocation_id) as affected_runs
FROM 'templates.parquet'
WHERE is_stack_trace = true OR level IN ('ERROR', 'FATAL')
GROUP BY template, level
ORDER BY total_occurrences DESC
LIMIT 20;

### Test suite pass rate and execution time trend:
SELECT command, family,
       AVG(passed) as avg_passed,
       AVG(failed) as avg_failed,
       AVG(total) as avg_total,
       COUNT(*) as test_runs
FROM 'test_results.parquet'
GROUP BY command, family;`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "agentsh-analytics: %v\n", err)
	os.Exit(1)
}
