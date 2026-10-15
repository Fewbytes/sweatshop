package gitx

import (
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestWrittenRepoFilesAreReadableByOtherUsers pins git's modes under a
// restrictive umask so another uid can clone the repository by path.
func TestWrittenRepoFilesAreReadableByOtherUsers(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	ctx := t.Context()
	r := Repo{Dir: t.TempDir()}
	if err := r.Init(ctx, true); err != nil {
		t.Fatal(err)
	}
	blob, err := r.WriteBlob(ctx, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := r.WriteTreeWithBlob(ctx, "testament", blob)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := r.CommitTree(ctx, tree, "", "msg", "t <t@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetRef(ctx, "refs/heads/master", commit); err != nil {
		t.Fatal(err)
	}

	err = filepath.WalkDir(r.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(r.Dir, path)
		got := info.Mode().Perm()
		switch {
		case d.IsDir() && got != 0o755:
			t.Errorf("%s mode = %o, want 755", rel, got)
		case !d.IsDir() && strings.HasPrefix(rel, "objects/") && got != 0o444:
			t.Errorf("%s mode = %o, want 444", rel, got)
		case !d.IsDir() && !strings.HasPrefix(rel, "objects/") && got&0o044 != 0o044:
			t.Errorf("%s mode = %o, want world-readable", rel, got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
