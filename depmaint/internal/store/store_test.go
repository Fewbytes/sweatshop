package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/snapshot"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "cache.db")
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestInsertAndLatestSnapshot(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	snap := snapshot.Snapshot{ID: "abc123", Root: "/repo", CreatedAt: "2026-01-01T00:00:00Z", NodeCount: 1, EdgeCount: 0}
	nodes := []expander.Node{{Key: "go:a@1.0.0", Kind: "package", Direct: true}}
	if err := s.InsertSnapshot(ctx, snap, nodes, nil); err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}

	got, ok, err := s.LatestSnapshot(ctx, "/repo")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if !ok {
		t.Fatal("LatestSnapshot: not found")
	}
	if got.ID != "abc123" {
		t.Errorf("ID = %q, want abc123", got.ID)
	}

	gotNodes, err := s.SnapshotNodes(ctx, "abc123")
	if err != nil {
		t.Fatalf("SnapshotNodes: %v", err)
	}
	if len(gotNodes) != 1 || gotNodes[0].Key != "go:a@1.0.0" {
		t.Errorf("SnapshotNodes = %v", gotNodes)
	}
}

func TestLatestSnapshotNoneFound(t *testing.T) {
	s := openTestStore(t)
	_, ok, err := s.LatestSnapshot(context.Background(), "/nope")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if ok {
		t.Error("expected not found")
	}
}

func TestInsertSnapshotIsIdempotentForSameID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	snap := snapshot.Snapshot{ID: "dup1", Root: "/repo", CreatedAt: "2026-01-01T00:00:00Z"}
	nodes := []expander.Node{{Key: "go:a@1.0.0", Kind: "package"}}
	if err := s.InsertSnapshot(ctx, snap, nodes, nil); err != nil {
		t.Fatalf("first InsertSnapshot: %v", err)
	}
	if err := s.InsertSnapshot(ctx, snap, nodes, nil); err != nil {
		t.Fatalf("second InsertSnapshot (same id) must not error: %v", err)
	}
}

func TestPruneSnapshotsKeepsNewest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i, id := range []string{"s1", "s2", "s3"} {
		snap := snapshot.Snapshot{ID: id, Root: "/repo", CreatedAt: []string{"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z"}[i]}
		if err := s.InsertSnapshot(ctx, snap, nil, nil); err != nil {
			t.Fatalf("InsertSnapshot %s: %v", id, err)
		}
	}
	if err := s.PruneSnapshots(ctx, "/repo", 1); err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	got, ok, err := s.LatestSnapshot(ctx, "/repo")
	if err != nil || !ok {
		t.Fatalf("LatestSnapshot after prune: ok=%v err=%v", ok, err)
	}
	if got.ID != "s3" {
		t.Errorf("LatestSnapshot after prune = %q, want s3", got.ID)
	}
}
