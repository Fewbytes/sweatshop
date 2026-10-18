// Package store persists graph snapshots, nodes, edges, and enrichment in
// depmaint's local, disposable SQLite cache. The API is written against
// database/sql so the underlying engine can change without touching callers.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/enrich"
	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/snapshot"

	_ "turso.tech/database/tursogo"
)

//go:embed schema.sql
var schema string

// Store is a handle to the depmaint cache database.
type Store struct{ db *sql.DB }

// Open connects to the database at dsn.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("turso", dsn)
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

// InsertSnapshot writes a snapshot and its nodes/edges. Snapshots are
// content-addressed and immutable, so writing an id that already exists is
// a no-op, not an error — the caller re-expanded an unchanged tree.
func (s *Store) InsertSnapshot(ctx context.Context, snap snapshot.Snapshot, nodes []expander.Node, edges []expander.Edge) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO snapshot (id, root, created_at, expander, node_count, edge_count)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		snap.ID, snap.Root, snap.CreatedAt, snap.Expanders, snap.NodeCount, snap.EdgeCount)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		_, err = tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO node (snapshot_id, key, kind, direct) VALUES (?, ?, ?, ?)`,
			snap.ID, n.Key, n.Kind, n.Direct)
		if err != nil {
			return err
		}
	}
	for _, e := range edges {
		_, err = tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO edge (snapshot_id, from_key, to_key, requirement, resolved, internal)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			snap.ID, e.From, e.To, e.Requirement, e.Resolved, e.Internal)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LatestSnapshot returns the most recently created snapshot for root.
func (s *Store) LatestSnapshot(ctx context.Context, root string) (snapshot.Snapshot, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, root, created_at, expander, node_count, edge_count FROM snapshot
		 WHERE root = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, root)
	var snap snapshot.Snapshot
	err := row.Scan(&snap.ID, &snap.Root, &snap.CreatedAt, &snap.Expanders, &snap.NodeCount, &snap.EdgeCount)
	if err == sql.ErrNoRows {
		return snapshot.Snapshot{}, false, nil
	}
	if err != nil {
		return snapshot.Snapshot{}, false, err
	}
	return snap, true, nil
}

// SnapshotNodes returns every node in a snapshot.
func (s *Store) SnapshotNodes(ctx context.Context, snapshotID string) ([]expander.Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, kind, direct FROM node WHERE snapshot_id = ? ORDER BY key`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []expander.Node
	for rows.Next() {
		var n expander.Node
		if err := rows.Scan(&n.Key, &n.Kind, &n.Direct); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// PruneSnapshots deletes every snapshot for root except the newest keep,
// along with their nodes and edges.
func (s *Store) PruneSnapshots(ctx context.Context, root string, keep int) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM snapshot WHERE root = ? ORDER BY created_at DESC, rowid DESC`, root)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if keep < 0 {
		keep = 0
	}
	if len(ids) <= keep {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids[keep:] {
		if _, err := tx.ExecContext(ctx, `DELETE FROM node WHERE snapshot_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM edge WHERE snapshot_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetEnrichment returns the cached payload for (source, key), if any.
func (s *Store) GetEnrichment(ctx context.Context, source enrich.Source, key string) (enrich.Row, bool, error) {
	var payload, fetchedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT payload, fetched_at FROM enrichment WHERE source = ? AND key = ?`,
		string(source), key).Scan(&payload, &fetchedAt)
	if err == sql.ErrNoRows {
		return enrich.Row{}, false, nil
	}
	if err != nil {
		return enrich.Row{}, false, err
	}
	t, err := time.Parse(time.RFC3339, fetchedAt)
	if err != nil {
		return enrich.Row{}, false, fmt.Errorf("enrichment %s %s: bad fetched_at %q: %w", source, key, fetchedAt, err)
	}
	return enrich.Row{Payload: []byte(payload), FetchedAt: t}, true, nil
}

// PutEnrichment stores or replaces the payload for (source, key).
func (s *Store) PutEnrichment(ctx context.Context, source enrich.Source, key string, row enrich.Row) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO enrichment (source, key, payload, fetched_at) VALUES (?, ?, ?, ?)`,
		string(source), key, string(row.Payload), row.FetchedAt.UTC().Format(time.RFC3339))
	return err
}
