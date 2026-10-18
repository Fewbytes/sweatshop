// Package linestore is the line's master database: witness runs, per-test
// results, Kaizen signal baselines and heijunka levels in one embedded Turso
// file (design-docs/assembly-line-spec.md, "Runtime and storage").
//
// The line server (`sweatshop line serve`) is the only process that opens the
// database; stations and workers reach it through the station API. A Store
// holds a single connection, so its methods are safe for concurrent use and
// serialise writes.
package linestore

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "turso.tech/database/tursogo"

	"github.com/Fewbytes/sweatshop/line/report"
)

//go:embed schema.sql
var schema string

// FileName is the database's name inside the line directory.
const FileName = "line.db"

// Levels is the capacity the heijunka worker publishes.
type Levels struct {
	Agents   int `json:"agents"`    // maker stations that should be running
	WIPLimit int `json:"wip_limit"` // cards allowed in progress at once
}

// KaizenBaseline is one stored Kaizen measurement: the signals.json of the
// master commit it was measured on.
type KaizenBaseline struct {
	Commit  string
	Signals json.RawMessage
}

// MutationSource says what a mutation measurement was taken for.
type MutationSource string

const (
	MutationFromKaizenRun  MutationSource = "kaizen"
	MutationFromLandedCard MutationSource = "card"
)

// MutationRun is one mutation measurement of Scope (Go package directories or
// source files) at Commit. Score and SampleSize are nil, and Unavailable says
// why, when the tool could not finish; ToolVersion is empty when the tool was
// not installed. Mutation score is tracked, never gated on.
type MutationRun struct {
	Commit      string         `json:"commit"`
	Source      MutationSource `json:"source"`
	Language    string         `json:"language"`
	Scope       []string       `json:"scope"`
	Tool        string         `json:"tool"`
	ToolVersion string         `json:"tool_version"`
	DurationMS  int64          `json:"duration_ms"`
	SampleSize  *int           `json:"sample_size"` // mutants that counted towards Score
	Score       *float64       `json:"score"`       // in [0, 1], higher is better
	Unavailable string         `json:"unavailable"`
}

// Store is the line database.
type Store struct{ db *sql.DB }

// Open opens (creating it if needed) the database file at path and applies
// the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("turso", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// The driver runs one statement per Exec; schema.sql has no semicolons
	// outside statement ends.
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply line database schema %q: %w", stmt, err)
		}
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("stored time %q: %w", s, err)
	}
	return t, nil
}

// RecordRun saves one witness run's statistics. It implements report.RunStore.
func (s *Store) RecordRun(ctx context.Context, st report.Stats) error {
	return insertRun(ctx, s.db, st)
}

// RecordTests saves one run's per-test results in one transaction. It
// implements report.TestStore.
func (s *Store) RecordTests(ctx context.Context, rows []report.TestRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertTests(ctx, tx, rows); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordChecks saves one run's per-check results in one transaction. It
// implements report.CheckStore.
func (s *Store) RecordChecks(ctx context.Context, rows []report.CheckRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO check_result (run_id, commit_sha, tree, role, started_at, check_name, exit_code, duration_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.RunID, r.Commit, r.Tree, r.Role, formatTime(r.StartedAt), r.Check, r.ExitCode, r.DurationMS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// execer is the part of *sql.DB and *sql.Tx the inserts need.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertRun(ctx context.Context, db execer, st report.Stats) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO run (run_id, commit_sha, tree, role, agent_id, runner, started_at, duration_ms, checks, failures, passed, outcome, report_key, html_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.RunID, st.Commit, st.Tree, st.Role, st.AgentID, st.Runner, formatTime(st.StartedAt), st.DurationMS,
		st.Checks, st.Failures, boolInt(st.Passed), string(st.Outcome), st.ReportKey, st.HTMLKey)
	return err
}

func insertTests(ctx context.Context, db execer, rows []report.TestRow) error {
	for _, r := range rows {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO test_result (run_id, commit_sha, tree, role, started_at, check_name, package, test, outcome, duration_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.RunID, r.Commit, r.Tree, r.Role, formatTime(r.StartedAt), r.Check, r.Package, r.Test, string(r.Outcome), r.DurationMS); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Runs returns every saved run, oldest first.
func (s *Store) Runs(ctx context.Context) ([]report.Stats, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, commit_sha, tree, role, agent_id, runner, started_at, duration_ms, checks, failures, passed, outcome, report_key, html_key
		 FROM run ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []report.Stats
	for rows.Next() {
		var st report.Stats
		var started, outcome string
		var passed int
		if err := rows.Scan(&st.RunID, &st.Commit, &st.Tree, &st.Role, &st.AgentID, &st.Runner, &started, &st.DurationMS,
			&st.Checks, &st.Failures, &passed, &outcome, &st.ReportKey, &st.HTMLKey); err != nil {
			return nil, err
		}
		if st.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		st.Passed, st.Outcome = passed != 0, report.RunOutcome(outcome)
		out = append(out, st)
	}
	return out, rows.Err()
}

// Tests returns every saved test result, oldest first.
func (s *Store) Tests(ctx context.Context) ([]report.TestRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, commit_sha, tree, role, started_at, check_name, package, test, outcome, duration_ms
		 FROM test_result ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []report.TestRow
	for rows.Next() {
		var r report.TestRow
		var started, outcome string
		if err := rows.Scan(&r.RunID, &r.Commit, &r.Tree, &r.Role, &started, &r.Check, &r.Package, &r.Test, &outcome, &r.DurationMS); err != nil {
			return nil, err
		}
		if r.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		r.Outcome = report.TestOutcome(outcome)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Checks returns every saved check result, oldest first.
func (s *Store) Checks(ctx context.Context) ([]report.CheckRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, commit_sha, tree, role, started_at, check_name, exit_code, duration_ms
		 FROM check_result ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []report.CheckRow
	for rows.Next() {
		var r report.CheckRow
		var started string
		if err := rows.Scan(&r.RunID, &r.Commit, &r.Tree, &r.Role, &started, &r.Check, &r.ExitCode, &r.DurationMS); err != nil {
			return nil, err
		}
		if r.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CommitPassed reports whether a witness run passed on commit.
func (s *Store) CommitPassed(ctx context.Context, commit string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run WHERE commit_sha = ? AND passed = 1`, commit).Scan(&n)
	return n > 0, err
}

// AddKaizenBaseline records a baseline unless its commit already has one:
// publishing again after a change must not double the history.
func (s *Store) AddKaizenBaseline(ctx context.Context, b KaizenBaseline) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO kaizen_baseline (commit_sha, signals) VALUES (?, ?)`, b.Commit, string(b.Signals))
	return err
}

// PreviousKaizenBaseline returns the newest baseline measured at a commit
// other than the given one; nil when there is none.
func (s *Store) PreviousKaizenBaseline(ctx context.Context, commit string) (*KaizenBaseline, error) {
	var b KaizenBaseline
	var signals string
	err := s.db.QueryRowContext(ctx,
		`SELECT commit_sha, signals FROM kaizen_baseline WHERE commit_sha <> ? ORDER BY seq DESC LIMIT 1`, commit).Scan(&b.Commit, &signals)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Signals = json.RawMessage(signals)
	return &b, nil
}

// SetLevels replaces the published heijunka levels.
func (s *Store) SetLevels(ctx context.Context, l Levels) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO levels (id, agents, wip_limit) VALUES (1, ?, ?)`, l.Agents, l.WIPLimit)
	return err
}

// Levels returns the published heijunka levels, or zero Levels when none were
// published.
func (s *Store) Levels(ctx context.Context) (Levels, error) {
	var l Levels
	err := s.db.QueryRowContext(ctx, `SELECT agents, wip_limit FROM levels WHERE id = 1`).Scan(&l.Agents, &l.WIPLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return Levels{}, nil
	}
	return l, err
}

// RecordMutationRun saves one mutation measurement.
func (s *Store) RecordMutationRun(ctx context.Context, m MutationRun) error {
	scope, err := json.Marshal(m.Scope)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO mutation_run (commit_sha, source, language, scope, tool, tool_version, duration_ms, sample_size, score, unavailable)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Commit, string(m.Source), m.Language, string(scope), m.Tool, m.ToolVersion, m.DurationMS, m.SampleSize, m.Score, m.Unavailable)
	return err
}

// MutationRuns returns every saved mutation measurement, oldest first.
func (s *Store) MutationRuns(ctx context.Context) ([]MutationRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT commit_sha, source, language, scope, tool, tool_version, duration_ms, sample_size, score, unavailable
		 FROM mutation_run ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MutationRun{}
	for rows.Next() {
		var m MutationRun
		var source, scope string
		if err := rows.Scan(&m.Commit, &source, &m.Language, &scope, &m.Tool, &m.ToolVersion, &m.DurationMS, &m.SampleSize, &m.Score, &m.Unavailable); err != nil {
			return nil, err
		}
		m.Source = MutationSource(source)
		if err := json.Unmarshal([]byte(scope), &m.Scope); err != nil {
			return nil, fmt.Errorf("stored mutation scope %q: %w", scope, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
