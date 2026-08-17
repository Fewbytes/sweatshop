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
		db, _ = sql.Open("libsql", "file:"+config.Path)
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

func (s *Store) migrate(ctx context.Context) error {
	// libSQL manages the embedded database journal mode. PRAGMA journal_mode
	// returns a row and cannot be applied through the driver's Exec path.
	statements := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS sessions (
			name TEXT PRIMARY KEY, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS invocations (
			id TEXT PRIMARY KEY, session TEXT NOT NULL, pid INTEGER, argv TEXT NOT NULL,
			command TEXT NOT NULL, cwd TEXT NOT NULL, env_delta TEXT NOT NULL,
			stdin_sha256 TEXT, state TEXT NOT NULL, exit_code INTEGER, reason TEXT, signal INTEGER,
			started_at TEXT NOT NULL, ended_at TEXT, duration_ms INTEGER NOT NULL DEFAULT 0,
			stdout_ref TEXT NOT NULL, stderr_ref TEXT NOT NULL, cwd_after TEXT,
			env_after TEXT NOT NULL, paths_touched TEXT NOT NULL,
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
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply storage migration: %w", err)
		}
	}
	return nil
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
	result, err := s.db.ExecContext(ctx, `UPDATE invocations SET state=?,exit_code=?,reason=?,signal=?,ended_at=?,
		duration_ms=?,stdout_ref=?,stderr_ref=?,cwd_after=?,env_after=?,paths_touched=? WHERE id=?`,
		invocation.State, invocation.ExitCode, invocation.Reason, invocation.Signal,
		invocation.EndedAt.UTC().Format(time.RFC3339Nano), invocation.DurationMS, string(stdout), string(stderr),
		invocation.CWDAfter, string(envAfter), string(paths), invocation.ID)
	return changed(result, err, invocation.ID)
}

func (s *Store) History(ctx context.Context, session, command, since string, exit *int, limit int) ([]Invocation, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `SELECT id FROM invocations WHERE 1=1`
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
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var result []Invocation
	for _, id := range ids {
		item, err := s.GetInvocation(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Store) GetInvocation(ctx context.Context, id string) (Invocation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,session,pid,argv,command,cwd,env_delta,stdin_sha256,state,
		exit_code,reason,signal,started_at,ended_at,duration_ms,stdout_ref,stderr_ref,cwd_after,env_after,paths_touched
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
	var stdin, reason, endedAt, cwdAfter sql.NullString
	var argv, envDelta, stdout, stderr, envAfter, paths, startedAt string
	err := row.Scan(&value.ID, &value.Session, &pid, &argv, &value.Command, &value.CWD, &envDelta, &stdin,
		&value.State, &exitCode, &reason, &signal, &startedAt, &endedAt, &value.DurationMS, &stdout,
		&stderr, &cwdAfter, &envAfter, &paths)
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
