package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	libsql "github.com/tursodatabase/go-libsql"
)

type Config struct {
	Path         string
	RemoteURL    string
	AuthToken    string
	SyncInterval time.Duration
}

type Store struct {
	db        *sql.DB
	connector *libsql.Connector
}

func Open(ctx context.Context, config Config) (*Store, error) {
	if config.Path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(config.Path), 0o700); err != nil {
		return nil, err
	}

	var db *sql.DB
	var connector *libsql.Connector
	if config.RemoteURL == "" {
		var err error
		db, err = sql.Open("libsql", "file:"+config.Path)
		if err != nil {
			return nil, fmt.Errorf("open local database: %w", err)
		}
	} else {
		opts := []libsql.Option{libsql.WithAuthToken(config.AuthToken), libsql.WithReadYourWrites(true)}
		if config.SyncInterval > 0 {
			opts = append(opts, libsql.WithSyncInterval(config.SyncInterval))
		}
		var err error
		connector, err = libsql.NewEmbeddedReplicaConnector(config.Path, config.RemoteURL, opts...)
		if err != nil {
			return nil, fmt.Errorf("create Turso connector: %w", err)
		}
		db = sql.OpenDB(connector)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, connector: connector}
	// Wait for a competing writer instead of failing instantly. Queried rather
	// than Exec'd because the pragma may return the resulting value as a row.
	if rows, pragmaErr := db.QueryContext(ctx, `PRAGMA busy_timeout=5000`); pragmaErr == nil {
		_ = rows.Close()
	}
	if err := store.migrate(ctx); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// migration is one forward step in the schema. Steps are applied in order,
// each in its own transaction, and the highest applied version is recorded
// in schema_version so a restart only runs what's new.
type migration struct {
	version int
	apply   func(ctx context.Context, tx *sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		apply: func(ctx context.Context, tx *sql.Tx) error {
			// libSQL manages the embedded database journal mode. PRAGMA
			// journal_mode returns a row and cannot be applied through the
			// driver's Exec path.
			statements := []string{
				`CREATE TABLE IF NOT EXISTS sessions (
					name TEXT PRIMARY KEY, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
				)`,
				`CREATE TABLE IF NOT EXISTS invocations (
					id TEXT PRIMARY KEY, session TEXT NOT NULL, pid INTEGER, argv TEXT NOT NULL,
					command TEXT NOT NULL, cwd TEXT NOT NULL, env_delta TEXT NOT NULL,
					stdin_sha256 TEXT, state TEXT NOT NULL, exit_code INTEGER, reason TEXT, signal INTEGER,
					started_at TEXT NOT NULL, ended_at TEXT, duration_ms INTEGER NOT NULL DEFAULT 0,
					stdout_ref TEXT NOT NULL, stderr_ref TEXT NOT NULL, cwd_after TEXT,
					env_after TEXT NOT NULL, paths_touched TEXT NOT NULL, summary TEXT,
					FOREIGN KEY(session) REFERENCES sessions(name)
				)`,
				`CREATE INDEX IF NOT EXISTS invocations_session_started ON invocations(session, started_at)`,
				`CREATE INDEX IF NOT EXISTS invocations_exit_code ON invocations(exit_code)`,
				`CREATE VIRTUAL TABLE IF NOT EXISTS invocation_commands USING fts5(id UNINDEXED, command)`,
				`CREATE TRIGGER IF NOT EXISTS invocation_commands_insert AFTER INSERT ON invocations BEGIN
					INSERT INTO invocation_commands(id, command) VALUES (new.id, new.command);
				END`,
				`CREATE TRIGGER IF NOT EXISTS invocation_commands_update AFTER UPDATE OF command ON invocations BEGIN
					DELETE FROM invocation_commands WHERE id = old.id;
					INSERT INTO invocation_commands(id, command) VALUES (new.id, new.command);
				END`,
				`CREATE TRIGGER IF NOT EXISTS invocation_commands_delete AFTER DELETE ON invocations BEGIN
					DELETE FROM invocation_commands WHERE id = old.id;
				END`,
				`CREATE TABLE IF NOT EXISTS log_templates (
					invocation_id TEXT NOT NULL,
					stream TEXT NOT NULL,
					template_id TEXT NOT NULL,
					template TEXT NOT NULL,
					count INTEGER NOT NULL,
					first_line INTEGER NOT NULL,
					last_line INTEGER NOT NULL,
					exemplar_offset INTEGER NOT NULL DEFAULT 0,
					exemplar TEXT NOT NULL DEFAULT '',
					level TEXT NOT NULL DEFAULT '',
					is_stack_trace INTEGER NOT NULL DEFAULT 0,
					PRIMARY KEY(invocation_id, stream, template_id)
				)`,
				`CREATE INDEX IF NOT EXISTS log_templates_template ON log_templates(template_id)`,
			}
			for _, statement := range statements {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
			// Databases created before schema_version existed may already
			// have gone through the old ad-hoc `ALTER TABLE ... ADD COLUMN
			// summary`, in which case the column already exists here and
			// this is a no-op; check first since ALTER errors on a
			// duplicate column.
			has, err := hasColumn(ctx, tx, "invocations", "summary")
			if err != nil {
				return err
			}
			if !has {
				if _, err := tx.ExecContext(ctx, `ALTER TABLE invocations ADD COLUMN summary TEXT`); err != nil {
					return err
				}
			}
			return nil
		},
	},
}

func hasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return false, err
	}
	// PRAGMA table_info columns: cid, name, type, notnull, dflt_value, pk
	dest := make([]any, len(cols))
	for i := range dest {
		dest[i] = new(sql.RawBytes)
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return false, err
		}
		if name, ok := dest[1].(*sql.RawBytes); ok && string(*name) == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	current := 0
	row := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version LIMIT 1`)
	if err := row.Scan(&current); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read schema version: %w", err)
	}
	haveRow := current > 0

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m, haveRow); err != nil {
			return fmt.Errorf("apply schema migration %d: %w", m.version, err)
		}
		current = m.version
		haveRow = true
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, m migration, haveRow bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := m.apply(ctx, tx); err != nil {
		return err
	}
	if haveRow {
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version=?`, m.version); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, m.version); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateInvocation(ctx context.Context, invocation Invocation) error {
	if invocation.ID == "" || invocation.Session == "" || invocation.Command == "" {
		return errors.New("invocation id, session, and command are required")
	}
	if invocation.State == "" {
		invocation.State = StateRunning
	}
	if invocation.State != StateRunning {
		return errors.New("new invocation must be in running state")
	}
	if invocation.StartedAt.IsZero() {
		invocation.StartedAt = time.Now().UTC()
	}
	argv, _ := json.Marshal(invocation.Argv)
	envDelta, _ := json.Marshal(nonNilMap(invocation.EnvDelta))
	stdout, _ := json.Marshal(invocation.Stdout)
	stderr, _ := json.Marshal(invocation.Stderr)
	envAfter, _ := json.Marshal(nonNilMap(invocation.EnvAfter))
	paths, _ := json.Marshal(nonNilSlice(invocation.PathsTouched))
	now := invocation.StartedAt.Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(name, created_at, updated_at) VALUES(?,?,?)
		ON CONFLICT(name) DO UPDATE SET updated_at=excluded.updated_at`, invocation.Session, now, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO invocations(
		id,session,pid,argv,command,cwd,env_delta,stdin_sha256,state,exit_code,reason,signal,
		started_at,ended_at,duration_ms,stdout_ref,stderr_ref,cwd_after,env_after,paths_touched
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, invocation.ID, invocation.Session, invocation.PID,
		string(argv), invocation.Command, invocation.CWD, string(envDelta), invocation.StdinSHA256,
		invocation.State, invocation.ExitCode, invocation.Reason, invocation.Signal, now, nil,
		invocation.DurationMS, string(stdout), string(stderr), invocation.CWDAfter, string(envAfter), string(paths))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetPID(ctx context.Context, id string, pid int) error {
	result, err := s.db.ExecContext(ctx, `UPDATE invocations SET pid=? WHERE id=? AND state=?`, pid, id, StateRunning)
	return changed(result, err, id)
}

func (s *Store) FinishInvocation(ctx context.Context, invocation Invocation) error {
	if invocation.State == StateRunning || invocation.State == "" || invocation.EndedAt == nil {
		return errors.New("terminal state and ended_at are required")
	}
	stdout, _ := json.Marshal(invocation.Stdout)
	stderr, _ := json.Marshal(invocation.Stderr)
	envAfter, _ := json.Marshal(nonNilMap(invocation.EnvAfter))
	paths, _ := json.Marshal(nonNilSlice(invocation.PathsTouched))
	var summary sql.NullString
	if invocation.Summary != nil {
		if b, err := json.Marshal(invocation.Summary); err == nil {
			summary = sql.NullString{String: string(b), Valid: true}
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE invocations SET state=?,exit_code=?,reason=?,signal=?,ended_at=?,
		duration_ms=?,stdout_ref=?,stderr_ref=?,cwd_after=?,env_after=?,paths_touched=?,summary=? WHERE id=?`,
		invocation.State, invocation.ExitCode, invocation.Reason, invocation.Signal,
		invocation.EndedAt.UTC().Format(time.RFC3339Nano), invocation.DurationMS, string(stdout), string(stderr),
		invocation.CWDAfter, string(envAfter), string(paths), summary, invocation.ID)
	return changed(result, err, invocation.ID)
}

// queryCounterKey, WithQueryCounter, and countQuery exist to let tests
// assert that History() issues exactly one query regardless of result size
// (it used to run one query per row — see sweatshop-c9h). They are inert
// unless a test installs a counter into the context.
type queryCounterKey struct{}

// WithQueryCounter returns a context that counts s.db queries made through
// it. Intended for tests.
func WithQueryCounter(ctx context.Context) (context.Context, *int64) {
	var n int64
	return context.WithValue(ctx, queryCounterKey{}, &n), &n
}

func countQuery(ctx context.Context) {
	if n, ok := ctx.Value(queryCounterKey{}).(*int64); ok {
		atomic.AddInt64(n, 1)
	}
}

func (s *Store) History(ctx context.Context, session, command, since string, exit *int, limit int) ([]Invocation, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `SELECT id,session,pid,argv,command,cwd,env_delta,stdin_sha256,state,
		exit_code,reason,signal,started_at,ended_at,duration_ms,stdout_ref,stderr_ref,cwd_after,env_after,paths_touched,summary
		FROM invocations WHERE 1=1`
	args := []any{}
	if session != "" {
		query += ` AND session=?`
		args = append(args, session)
	}
	if command != "" {
		query += ` AND id IN (SELECT id FROM invocation_commands WHERE invocation_commands MATCH ?)`
		args = append(args, command)
	}
	if exit != nil {
		query += ` AND exit_code=?`
		args = append(args, *exit)
	}
	if since != "" {
		query += ` AND started_at>=?`
		args = append(args, since)
	}
	query += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)
	countQuery(ctx)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Invocation
	for rows.Next() {
		item, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) GetInvocation(ctx context.Context, id string) (Invocation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,session,pid,argv,command,cwd,env_delta,stdin_sha256,state,
		exit_code,reason,signal,started_at,ended_at,duration_ms,stdout_ref,stderr_ref,cwd_after,env_after,paths_touched,summary
		FROM invocations WHERE id=?`, id)
	return scanInvocation(row)
}

func (s *Store) Reconcile(ctx context.Context, alive func(int) bool) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,pid FROM invocations WHERE state=?`, StateRunning)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var lost []string
	for rows.Next() {
		var id string
		var pid sql.NullInt64
		if err := rows.Scan(&id, &pid); err != nil {
			return 0, err
		}
		if !pid.Valid || !alive(int(pid.Int64)) {
			lost = append(lost, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(lost) == 0 {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(lost)), ",")
	args := []any{StateDaemonLost, "daemon_lost", now}
	for _, id := range lost {
		args = append(args, id)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE invocations SET state=?,reason=?,ended_at=? WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func changed(result sql.Result, err error, id string) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("invocation %q not found or invalid state", id)
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanInvocation(row scanner) (Invocation, error) {
	var value Invocation
	var pid, exitCode, signal sql.NullInt64
	var stdin, reason, endedAt, cwdAfter, summary sql.NullString
	var argv, envDelta, stdout, stderr, envAfter, paths, startedAt string
	err := row.Scan(&value.ID, &value.Session, &pid, &argv, &value.Command, &value.CWD, &envDelta, &stdin,
		&value.State, &exitCode, &reason, &signal, &startedAt, &endedAt, &value.DurationMS, &stdout,
		&stderr, &cwdAfter, &envAfter, &paths, &summary)
	if err != nil {
		return value, err
	}
	value.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return value, err
	}
	if endedAt.Valid {
		t, parseErr := time.Parse(time.RFC3339Nano, endedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.EndedAt = &t
	}
	if pid.Valid {
		n := int(pid.Int64)
		value.PID = &n
	}
	if exitCode.Valid {
		n := int(exitCode.Int64)
		value.ExitCode = &n
	}
	if signal.Valid {
		n := int(signal.Int64)
		value.Signal = &n
	}
	if stdin.Valid {
		value.StdinSHA256 = &stdin.String
	}
	if reason.Valid {
		value.Reason = &reason.String
	}
	if cwdAfter.Valid {
		value.CWDAfter = &cwdAfter.String
	}
	if summary.Valid && summary.String != "" {
		var s CommandSummary
		if err := json.Unmarshal([]byte(summary.String), &s); err == nil {
			value.Summary = &s
		}
	}
	for _, item := range []struct {
		raw    string
		target any
	}{{argv, &value.Argv}, {envDelta, &value.EnvDelta}, {stdout, &value.Stdout}, {stderr, &value.Stderr}, {envAfter, &value.EnvAfter}, {paths, &value.PathsTouched}} {
		if err := json.Unmarshal([]byte(item.raw), item.target); err != nil {
			return value, err
		}
	}
	return value, nil
}

type StoredLogTemplate struct {
	InvocationID   string `json:"invocation_id"`
	Stream         string `json:"stream"`
	TemplateID     string `json:"template_id"`
	Template       string `json:"template"`
	Count          int    `json:"count"`
	FirstLine      int    `json:"first_line"`
	LastLine       int    `json:"last_line"`
	ExemplarOffset int64  `json:"exemplar_offset"`
	Exemplar       string `json:"exemplar"`
	Level          string `json:"level"`
	IsStackTrace   bool   `json:"is_stack_trace"`
}

func (s *Store) SaveLogTemplates(ctx context.Context, invocationID, stream string, templates []StoredLogTemplate) error {
	if len(templates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO log_templates(
		invocation_id, stream, template_id, template, count, first_line, last_line,
		exemplar_offset, exemplar, level, is_stack_trace
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range templates {
		st := 0
		if t.IsStackTrace {
			st = 1
		}
		if _, err := stmt.ExecContext(ctx, invocationID, stream, t.TemplateID, t.Template, t.Count,
			t.FirstLine, t.LastLine, t.ExemplarOffset, t.Exemplar, t.Level, st); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetLogTemplates(ctx context.Context, invocationID, stream string) ([]StoredLogTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT invocation_id, stream, template_id, template, count,
		first_line, last_line, exemplar_offset, exemplar, level, is_stack_trace
		FROM log_templates WHERE invocation_id=? AND stream=? ORDER BY count DESC`, invocationID, stream)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StoredLogTemplate
	for rows.Next() {
		var t StoredLogTemplate
		var st int
		if err := rows.Scan(&t.InvocationID, &t.Stream, &t.TemplateID, &t.Template, &t.Count,
			&t.FirstLine, &t.LastLine, &t.ExemplarOffset, &t.Exemplar, &t.Level, &st); err != nil {
			return nil, err
		}
		t.IsStackTrace = (st != 0)
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Store) GetPriorTemplatesForCommand(ctx context.Context, command, excludeInvocationID string) (map[string]int, int, error) {
	var priorRunCount int
	countRow := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM invocations WHERE command=? AND id!=? AND state=?`,
		command, excludeInvocationID, StateExited)
	if err := countRow.Scan(&priorRunCount); err != nil {
		return nil, 0, err
	}
	if priorRunCount == 0 {
		return make(map[string]int), 0, nil
	}

	// Joined against invocations directly instead of collecting IDs into an
	// IN (...) clause: a command run thousands of times would otherwise blow
	// past SQLite's ~999 bound-parameter limit and this comparison would
	// start silently failing for exactly the commands that run most often.
	rows, err := s.db.QueryContext(ctx, `
		SELECT lt.template_id, SUM(lt.count)
		FROM log_templates lt
		JOIN invocations i ON i.id = lt.invocation_id
		WHERE i.command=? AND i.id!=? AND i.state=?
		GROUP BY lt.template_id`, command, excludeInvocationID, StateExited)
	if err != nil {
		return nil, priorRunCount, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var tmplID string
		var sum int
		if err := rows.Scan(&tmplID, &sum); err != nil {
			return nil, priorRunCount, err
		}
		counts[tmplID] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, priorRunCount, err
	}
	return counts, priorRunCount, nil
}

func nonNilMap(value map[string]string) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	return value
}
func nonNilSlice(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}
