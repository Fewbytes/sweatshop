// Package store persists runs, findings, and rules. The API is written against
// database/sql so the underlying engine can change without touching callers.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// Status is the triage state of a finding.
type Status string

const (
	StatusNew            Status = "new"
	StatusConfirmed      Status = "confirmed"
	StatusFixed          Status = "fixed"
	StatusUnreproducible Status = "unreproducible"
	StatusSuppressed     Status = "suppressed"
	StatusEscalated      Status = "escalated"
)

// Gates reports whether a finding in this status causes scan to exit non-zero.
// Only untriaged and confirmed-open findings gate; everything else is either
// resolved, dismissed, or already tracked as work elsewhere.
func (s Status) Gates() bool {
	return s == StatusNew || s == StatusConfirmed
}

// Finding is one deduplicated problem, identified by fingerprint.
type Finding struct {
	Fingerprint  string
	RuleID       string
	Detector     string
	File         string
	Line         int
	Symbol       string
	Message      string
	Severity     string
	Confidence   float64
	Status       Status
	FirstSeenRun string
	LastSeenRun  string
	SeenCount    int
	TestPath     string
	TestKept     bool
	FixCommit    string
	Branch       string
	PRURL        string
	BeadID       string
	TriageNote   string
}

// Run is one execution of scan or hunt.
type Run struct {
	ID                   string
	StartedAt            string
	CommitSHA            string
	Mode                 string
	DiffBase             string
	DetectorsUsed        []string
	DetectorsUnavailable []string
	AgentshInvocationIDs []string
	FindingCount         int
	NewCount             int
}

// Store is a handle to the bughunt database.
type Store struct{ db *sql.DB }

// Open connects to the database at dsn.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Migrate creates any missing tables. Safe to call repeatedly.
func (s *Store) Migrate(ctx context.Context) error {
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// InsertRun records one run.
func (s *Store) InsertRun(ctx context.Context, r Run) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run (id, started_at, commit_sha, mode, diff_base, detectors_used,
		 detectors_unavailable, agentsh_invocation_ids, finding_count, new_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.StartedAt, r.CommitSHA, r.Mode, r.DiffBase,
		strings.Join(r.DetectorsUsed, ","), strings.Join(r.DetectorsUnavailable, ","),
		strings.Join(r.AgentshInvocationIDs, ","), r.FindingCount, r.NewCount)
	return err
}

// UpsertFinding inserts a finding as new, or records another sighting of one
// already known. An existing finding keeps its triage status: a scan must never
// resurrect something a human already dismissed. It returns the status the
// finding holds after the write.
func (s *Store) UpsertFinding(ctx context.Context, f Finding) (Status, error) {
	existing, ok, err := s.GetFinding(ctx, f.Fingerprint)
	if err != nil {
		return "", err
	}
	if ok {
		_, err := s.db.ExecContext(ctx,
			`UPDATE finding SET line = ?, file = ?, message = ?, last_seen_run = ?,
			 seen_count = seen_count + 1 WHERE fingerprint = ?`,
			f.Line, f.File, f.Message, f.LastSeenRun, f.Fingerprint)
		if err != nil {
			return "", err
		}
		return existing.Status, nil
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO finding (fingerprint, rule_id, detector, file, line, symbol, message,
		 severity, confidence, status, first_seen_run, last_seen_run, seen_count,
		 test_path, test_kept, fix_commit, branch, pr_url, bead_id, triage_note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, '', false, '', '', '', '', '')`,
		f.Fingerprint, f.RuleID, f.Detector, f.File, f.Line, f.Symbol, f.Message,
		f.Severity, f.Confidence, StatusNew, f.FirstSeenRun, f.LastSeenRun)
	if err != nil {
		return "", err
	}
	return StatusNew, nil
}

// SetStatus records a triage decision.
func (s *Store) SetStatus(ctx context.Context, fingerprint string, status Status, note string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE finding SET status = ?, triage_note = ? WHERE fingerprint = ?`,
		status, note, fingerprint)
	return err
}

const findingColumns = `fingerprint, rule_id, detector, file, line, symbol, message,
 severity, confidence, status, first_seen_run, last_seen_run, seen_count,
 test_path, test_kept, fix_commit, branch, pr_url, bead_id, triage_note`

func scanFinding(rows interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	err := rows.Scan(&f.Fingerprint, &f.RuleID, &f.Detector, &f.File, &f.Line, &f.Symbol,
		&f.Message, &f.Severity, &f.Confidence, &f.Status, &f.FirstSeenRun, &f.LastSeenRun,
		&f.SeenCount, &f.TestPath, &f.TestKept, &f.FixCommit, &f.Branch, &f.PRURL,
		&f.BeadID, &f.TriageNote)
	return f, err
}

// GetFinding returns one finding by fingerprint.
func (s *Store) GetFinding(ctx context.Context, fingerprint string) (Finding, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+findingColumns+` FROM finding WHERE fingerprint = ?`, fingerprint)
	f, err := scanFinding(row)
	if err == sql.ErrNoRows {
		return Finding{}, false, nil
	}
	if err != nil {
		return Finding{}, false, err
	}
	return f, true, nil
}

// ListFindings returns findings, optionally filtered by status.
func (s *Store) ListFindings(ctx context.Context, status Status) ([]Finding, error) {
	query := `SELECT ` + findingColumns + ` FROM finding`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY fingerprint`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
