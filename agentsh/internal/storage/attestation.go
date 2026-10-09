package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Attestation is the witness's record of one attest run. Failed runs are kept
// too: they are evidence for the agent's track record.
type Attestation struct {
	ID        string
	Commit    string
	Tree      string
	Passed    bool
	Statement []byte
	Envelope  []byte
	CreatedAt time.Time
}

func (s *Store) SaveAttestation(ctx context.Context, a Attestation) error {
	var envelope any
	if a.Envelope != nil {
		envelope = string(a.Envelope)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO attestations(id, commit_sha, tree, passed, statement, envelope, created_at)
		VALUES(?,?,?,?,?,?,?)`, a.ID, a.Commit, a.Tree, a.Passed, string(a.Statement), envelope, a.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetAttestation(ctx context.Context, id string) (Attestation, error) {
	var a Attestation
	var statement, created string
	var envelope sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, commit_sha, tree, passed, statement, envelope, created_at
		FROM attestations WHERE id=?`, id).Scan(&a.ID, &a.Commit, &a.Tree, &a.Passed, &statement, &envelope, &created)
	if err != nil {
		return Attestation{}, fmt.Errorf("attestation %s: %w", id, err)
	}
	a.Statement = []byte(statement)
	if envelope.Valid {
		a.Envelope = []byte(envelope.String)
	}
	if a.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Attestation{}, err
	}
	return a, nil
}
