package cli

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

func TestAcceptance_InitGraphDriftDeleteRebuild(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	var initOut bytes.Buffer
	if err := Init(root, &initOut); err != nil {
		t.Fatalf("init: %v", err)
	}

	var g1 bytes.Buffer
	if err := Graph(ctx, root, true, &g1); err != nil {
		t.Fatalf("graph --refresh (1): %v", err)
	}
	id1 := latestSnapshotIDForTest(t, Paths(root).DB, root)

	var g2 bytes.Buffer
	if err := Graph(ctx, root, true, &g2); err != nil {
		t.Fatalf("graph --refresh (2): %v", err)
	}
	id2 := latestSnapshotIDForTest(t, Paths(root).DB, root)
	if id1 != id2 {
		t.Fatalf("unchanged tree produced different snapshot ids: %q != %q", id1, id2)
	}

	// Delete the cache and rebuild — must complete well within a minute and
	// reproduce the same snapshot id, since the (empty) registry didn't change.
	deadline := time.Now().Add(60 * time.Second)
	if err := os.RemoveAll(Paths(root).Dir); err != nil {
		t.Fatalf("removing cache: %v", err)
	}
	var reinitOut bytes.Buffer
	if err := Init(root, &reinitOut); err != nil {
		t.Fatalf("re-init: %v", err)
	}
	var g3 bytes.Buffer
	if err := Graph(ctx, root, true, &g3); err != nil {
		t.Fatalf("graph after rebuild: %v", err)
	}
	if time.Now().After(deadline) {
		t.Fatal("delete + rebuild exceeded 1 minute")
	}
	id3 := latestSnapshotIDForTest(t, Paths(root).DB, root)
	if id3 != id1 {
		t.Fatalf("rebuilt snapshot id differs: %q != %q", id3, id1)
	}
}
