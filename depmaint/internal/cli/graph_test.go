package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/store"
)

func TestGraphFailsWithoutInit(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Graph(context.Background(), root, false, &out); err == nil {
		t.Fatal("expected error when .depmaint/cache.db does not exist")
	}
}

func TestGraphUnchangedTreeSameSnapshotID(t *testing.T) {
	root := t.TempDir()
	var initOut bytes.Buffer
	if err := Init(root, &initOut); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var out1 bytes.Buffer
	if err := Graph(context.Background(), root, false, &out1); err != nil {
		t.Fatalf("first Graph: %v", err)
	}
	l := Paths(root)
	first := latestSnapshotIDForTest(t, l.DB, root)

	var out2 bytes.Buffer
	if err := Graph(context.Background(), root, true, &out2); err != nil {
		t.Fatalf("second Graph --refresh: %v", err)
	}
	second := latestSnapshotIDForTest(t, l.DB, root)

	if first != second {
		t.Errorf("snapshot id changed for unchanged (empty) registry: %q != %q", first, second)
	}
	if !strings.Contains(out2.String(), first) {
		t.Errorf("graph output should mention the snapshot id %q, got:\n%s", first, out2.String())
	}
}

func TestGraphReportsNoDriftOnFirstRun(t *testing.T) {
	root := t.TempDir()
	var initOut bytes.Buffer
	Init(root, &initOut)
	var out bytes.Buffer
	if err := Graph(context.Background(), root, false, &out); err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if !strings.Contains(out.String(), "0 nodes") {
		t.Errorf("expected empty-registry report to mention 0 nodes, got:\n%s", out.String())
	}
}

func latestSnapshotIDForTest(t *testing.T, dbPath, root string) string {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, ok, err := s.LatestSnapshot(context.Background(), root)
	if err != nil || !ok {
		t.Fatalf("LatestSnapshot: ok=%v err=%v", ok, err)
	}
	return snap.ID
}
