package goexpander

import (
	"os"
	"os/exec"
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

func writeGoMod(t *testing.T, dir, module string, requires ...string) {
	t.Helper()
	body := "module " + module + "\n\ngo 1.21\n"
	if len(requires) > 0 {
		body += "\nrequire (\n"
		for _, r := range requires {
			body += "\t" + r + "\n"
		}
		body += ")\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(requires) == 0 {
		return
	}
	// `go list -m -json all` refuses to run without go.sum entries for every
	// required module. `go mod download all` populates go.sum from the local
	// module cache (no network needed for modules already cached, which is
	// true here since this very depmaint module already requires yaml.v3).
	cmd := exec.Command("go", "mod", "download", "all")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod download all: %v\n%s", err, out)
	}
}

func TestExpandListsDirectDependency(t *testing.T) {
	root := t.TempDir()
	// A real, resolvable module with no further deps of its own, so this
	// test needs no network access beyond what the local module cache
	// already has from running `go test` in this repo (golang.org/x/text
	// is not guaranteed vendored — use a dependency already required by
	// this very depmaint module instead, so it's always in the local
	// module cache/build list without a network fetch).
	writeGoMod(t, root, "example.com/roottest", "gopkg.in/yaml.v3 v3.0.1")

	nodes, _, err := (Expander{}).Expand(root)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var found bool
	for _, n := range nodes {
		if n.Key == "go:gopkg.in/yaml.v3@v3.0.1" {
			found = true
			if n.Kind != "package" {
				t.Errorf("Kind = %q, want package", n.Kind)
			}
			if !n.Direct {
				t.Error("Direct = false, want true for a directly required module")
			}
		}
	}
	if !found {
		t.Fatalf("expected node go:gopkg.in/yaml.v3@v3.0.1, got %v", nodes)
	}
}

func TestExpandBuildsEdgeFromMainModule(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "example.com/edgetest", "gopkg.in/yaml.v3 v3.0.1")

	_, edges, err := (Expander{}).Expand(root)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var found bool
	for _, e := range edges {
		if e.From == "go:example.com/edgetest@" && e.To == "go:gopkg.in/yaml.v3@v3.0.1" {
			found = true
			if e.Resolved != "v3.0.1" {
				t.Errorf("Resolved = %q, want v3.0.1", e.Resolved)
			}
		}
	}
	if !found {
		t.Fatalf("expected an edge from the main module to go:gopkg.in/yaml.v3@v3.0.1, got %v", edges)
	}
}

func TestMainModulePath(t *testing.T) {
	root := t.TempDir()
	writeGoMod(t, root, "example.com/mainmodtest")
	got, err := MainModulePath(root)
	if err != nil {
		t.Fatalf("MainModulePath: %v", err)
	}
	if got != "example.com/mainmodtest" {
		t.Errorf("MainModulePath = %q, want example.com/mainmodtest", got)
	}
}
