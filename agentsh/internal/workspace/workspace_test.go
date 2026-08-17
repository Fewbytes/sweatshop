package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFindsGitWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := Resolve(nested)
	if err != nil {
		t.Fatal(err)
	}
	if paths.Root != root {
		t.Fatalf("root = %q, want %q", paths.Root, root)
	}
	if filepath.Dir(paths.Database) != paths.StateDir {
		t.Fatalf("database is outside state directory: %q", paths.Database)
	}
}
