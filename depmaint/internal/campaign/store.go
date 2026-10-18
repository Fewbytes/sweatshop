package campaign

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/planner"

	_ "turso.tech/database/tursogo"
)

//go:embed schema.sql
var schema string

// Store persists campaigns and their append-only attempt log. It is kept
// apart from the disposable graph cache because campaign state is a
// decision record, not regenerable data (spec: Storage).
type Store struct{ db *sql.DB }

// OpenStore connects to the campaign database at dsn and creates any missing
// tables.
func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("turso", dsn)
	if err != nil {
		return nil, err
	}
	for _, stmt := range strings.Split(schema, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const campaignColumns = `id, type, package, target, roots, status, max_attempts, max_seconds,
	spent, completed, plan_json, plan_hash, snapshot_id, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanCampaign(r rowScanner) (Campaign, error) {
	var c Campaign
	var roots, planJSON, created, updated string
	var maxSeconds int64
	err := r.Scan(&c.ID, &c.Type, &c.Package, &c.Target, &roots, &c.Status, &c.Budget.MaxAttempts, &maxSeconds,
		&c.Spent, &c.Completed, &planJSON, &c.PlanHash, &c.SnapshotID, &created, &updated)
	if err != nil {
		return Campaign{}, err
	}
	c.Budget.MaxDuration = time.Duration(maxSeconds) * time.Second
	if err := json.Unmarshal([]byte(roots), &c.Roots); err != nil {
		return Campaign{}, fmt.Errorf("campaign %s: bad roots: %w", c.ID, err)
	}
	if planJSON != "" {
		c.Plan = &planner.Plan{}
		if err := json.Unmarshal([]byte(planJSON), c.Plan); err != nil {
			return Campaign{}, fmt.Errorf("campaign %s: bad plan: %w", c.ID, err)
		}
	}
	if c.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return Campaign{}, err
	}
	if c.UpdatedAt, err = time.Parse(time.RFC3339, updated); err != nil {
		return Campaign{}, err
	}
	return c, nil
}

// Get returns the campaign with id, or an error if there is none.
func (s *Store) Get(ctx context.Context, id string) (Campaign, error) {
	c, err := scanCampaign(s.db.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM campaign WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Campaign{}, fmt.Errorf("no campaign %q", id)
	}
	return c, err
}

// List returns campaigns in creation order, optionally only those with status.
func (s *Store) List(ctx context.Context, status Status) ([]Campaign, error) {
	query, args := `SELECT `+campaignColumns+` FROM campaign`, []any{}
	if status != "" {
		query, args = query+` WHERE status = ?`, append(args, string(status))
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) count(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

// insert writes a new campaign row.
func (s *Store) insert(ctx context.Context, c Campaign) error {
	roots, err := json.Marshal(c.Roots)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO campaign (`+campaignColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, string(c.Type), c.Package, c.Target, string(roots), string(c.Status), c.Budget.MaxAttempts,
		int64(c.Budget.MaxDuration/time.Second), c.Spent, c.Completed, "", c.PlanHash, c.SnapshotID,
		c.CreatedAt.UTC().Format(time.RFC3339), c.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}

// save rewrites the mutable columns of c.
func (s *Store) save(ctx context.Context, c Campaign) error {
	planJSON := []byte{}
	if c.Plan != nil {
		var err error
		if planJSON, err = json.Marshal(c.Plan); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE campaign SET status = ?, spent = ?, completed = ?, plan_json = ?, plan_hash = ?, snapshot_id = ?, updated_at = ? WHERE id = ?`,
		string(c.Status), c.Spent, c.Completed, string(planJSON), c.PlanHash, c.SnapshotID,
		c.UpdatedAt.UTC().Format(time.RFC3339), c.ID)
	return err
}

// appendAttempt adds a row to the attempt log; rows are never updated.
func (s *Store) appendAttempt(ctx context.Context, a Attempt) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO attempt (id, campaign_id, step, plan_hash, snapshot_before, snapshot_after, verdict, reason, executor, pr_url, commit_sha, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.CampaignID, a.Step, a.PlanHash, a.SnapshotBefore, a.SnapshotAfter, string(a.Verdict), a.Reason,
		a.Executor, a.PRURL, a.CommitSHA, a.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// Attempts returns a campaign's attempt log in recording order.
func (s *Store) Attempts(ctx context.Context, campaignID string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, campaign_id, step, plan_hash, snapshot_before, snapshot_after, verdict, reason, executor, pr_url, commit_sha, created_at
		 FROM attempt WHERE campaign_id = ? ORDER BY created_at, id`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		var created string
		if err := rows.Scan(&a.ID, &a.CampaignID, &a.Step, &a.PlanHash, &a.SnapshotBefore, &a.SnapshotAfter,
			&a.Verdict, &a.Reason, &a.Executor, &a.PRURL, &a.CommitSHA, &created); err != nil {
			return nil, err
		}
		if a.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Escalation records that a campaign was handed to humans. BeadID is empty
// when no tracker was available; the row still documents the reason.
type Escalation struct {
	ID         string
	CampaignID string
	Reason     string
	BeadID     string
	CreatedAt  time.Time
}

// AppendEscalation adds a row to the escalation log; rows are never updated.
func (s *Store) AppendEscalation(ctx context.Context, e Escalation) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO escalation (id, campaign_id, reason, bead_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.CampaignID, e.Reason, e.BeadID, e.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// Escalations returns a campaign's escalations in recording order.
func (s *Store) Escalations(ctx context.Context, campaignID string) ([]Escalation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, campaign_id, reason, bead_id, created_at FROM escalation WHERE campaign_id = ? ORDER BY created_at, id`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Escalation
	for rows.Next() {
		var e Escalation
		var created string
		if err := rows.Scan(&e.ID, &e.CampaignID, &e.Reason, &e.BeadID, &created); err != nil {
			return nil, err
		}
		if e.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
