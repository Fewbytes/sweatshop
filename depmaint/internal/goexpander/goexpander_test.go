package goexpander

import (
	"os"
	"path/filepath"
	"testing"
)

func TestName(t *testing.T) {
	if got := (Expander{}).Name(); got != "go" {
		t.Errorf("Name() = %q, want %q", got, "go")
	}
}

func TestAvailableFindsGoToolchain(t *testing.T) {
	// The test suite itself only runs under `go test`, so the go toolchain
	// is guaranteed present in this environment.
	if !(Expander{}).Available() {
		t.Error("Available() = false, want true (go toolchain must be on PATH to run this test)")
	}
}

func TestExpandReturnsEmptyForNonGoRoot(t *testing.T) {
	root := t.TempDir() // no go.mod here
	nodes, edges, err := (Expander{}).Expand(root)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if nodes != nil || edges != nil {
		t.Errorf("Expand(non-Go root) = %v, %v, want nil, nil", nodes, edges)
	}
}

func TestExpandDetectsGoModPresence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/empty\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nodes, edges, err := (Expander{}).Expand(root)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// A module with no requires has no dependency nodes/edges, but Expand
	// must not treat "go.mod present, empty dep list" as "not a Go root".
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("Expand(empty go.mod) = %v, %v, want empty slices", nodes, edges)
	}
}
