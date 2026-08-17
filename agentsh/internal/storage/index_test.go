package storage

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildLineIndexCheckpoints(t *testing.T) {
	idx, err := BuildLineIndex(strings.NewReader("a\nb\nc\nd\ne\nf\ng\n"), 3, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Lines != 7 || idx.Bytes != 14 {
		t.Fatalf("lines/bytes = %d/%d, want 7/14", idx.Lines, idx.Bytes)
	}
	want := []IndexEntry{{Line: 1, Offset: 0}, {Line: 4, Offset: 6}, {Line: 7, Offset: 12}}
	if len(idx.Entries) != len(want) {
		t.Fatalf("entries = %+v", idx.Entries)
	}
	for i := range want {
		if idx.Entries[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, idx.Entries[i], want[i])
		}
	}
	for line, wantEntry := range map[int64]IndexEntry{
		3: {Line: 1, Offset: 0},
		6: {Line: 4, Offset: 6},
		7: {Line: 7, Offset: 12},
	} {
		if got := idx.Lookup(line); got != wantEntry {
			t.Fatalf("lookup(%d) = %+v, want %+v", line, got, wantEntry)
		}
	}
}

func TestBuildLineIndexEmptyAndUnterminated(t *testing.T) {
	idx, err := BuildLineIndex(strings.NewReader(""), DefaultCheckpointLines, DefaultCheckpointBytes)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Lines != 0 || len(idx.Entries) != 0 {
		t.Fatalf("empty stream index = %+v", idx)
	}
	idx, err = BuildLineIndex(strings.NewReader("a\nb"), DefaultCheckpointLines, DefaultCheckpointBytes)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Lines != 2 || idx.Bytes != 3 {
		t.Fatalf("unterminated stream index = %+v", idx)
	}
}

func TestBlobWriterCommitsIndexAndGCRemovesIt(t *testing.T) {
	root := t.TempDir()
	store := BlobStore{
		Root: filepath.Join(root, "blobs"), Index: filepath.Join(root, "index"),
		CheckpointLines: 3,
	}
	writer, err := store.NewWriter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "a\nb\nc\nd\ne\nf\ng\n"); err != nil {
		t.Fatal(err)
	}
	ref, err := writer.Commit()
	if err != nil {
		t.Fatal(err)
	}
	idx, err := store.LoadIndex(ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if idx == nil {
		t.Fatal("index missing after commit")
	}
	if idx.Lines != 7 || idx.Bytes != 14 {
		t.Fatalf("index metadata = %+v", idx)
	}
	want := []IndexEntry{{Line: 1, Offset: 0}, {Line: 4, Offset: 6}, {Line: 7, Offset: 12}}
	if len(idx.Entries) != len(want) {
		t.Fatalf("index entries = %+v", idx.Entries)
	}
	for i := range want {
		if idx.Entries[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, idx.Entries[i], want[i])
		}
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(store.Root, ref.SHA256), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GC(time.Minute, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.IndexPath(ref.SHA256)); !os.IsNotExist(err) {
		t.Fatalf("index survived blob GC: %v", err)
	}
}

func TestLoadIndexRejectsCorruptAndRebuildRecovers(t *testing.T) {
	root := t.TempDir()
	store := BlobStore{Root: filepath.Join(root, "blobs"), Index: filepath.Join(root, "index")}
	writer, err := store.NewWriter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "one\ntwo\nthree\n"); err != nil {
		t.Fatal(err)
	}
	ref, err := writer.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.IndexPath(ref.SHA256), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadIndex(ref.SHA256); err == nil {
		t.Fatal("expected corrupt index to fail load")
	}
	idx, err := store.Rebuild(ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Lines != 3 {
		t.Fatalf("rebuilt index lines = %d, want 3", idx.Lines)
	}
	reloaded, err := store.LoadIndex(ref.SHA256)
	if err != nil || reloaded == nil || reloaded.Lines != 3 {
		t.Fatalf("reload after rebuild = %+v, %v", reloaded, err)
	}
}

func TestLoadIndexRejectsUnknownVersion(t *testing.T) {
	root := t.TempDir()
	store := BlobStore{Index: filepath.Join(root, "index")}
	if err := os.MkdirAll(store.Index, 0o700); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, indexHeaderSize)
	copy(header[:7], indexMagic)
	header[7] = 99
	if err := os.WriteFile(store.IndexPath("deadbeef"), header, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadIndex("deadbeef"); err == nil {
		t.Fatal("expected unknown index version to fail load")
	}
}
