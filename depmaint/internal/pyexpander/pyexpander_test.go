package pyexpander

import (
	"os"
	"path/filepath"
	"testing"
)

func TestName(t *testing.T) {
	if got := (Expander{}).Name(); got != "python" {
		t.Errorf("Name() = %q, want %q", got, "python")
	}
}

func TestAvailableReflectsUvOnPath(t *testing.T) {
	// This just exercises the code path; it may be true or false depending
	// on whether `uv` happens to be installed in the environment running
	// this test. It must not panic or error.
	_ = (Expander{}).Available()
}

func TestExpandReturnsEmptyWithoutLockfile(t *testing.T) {
	root := t.TempDir() // no uv.lock here
	nodes, edges, err := (Expander{}).Expand(root)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if nodes != nil || edges != nil {
		t.Errorf("Expand(no uv.lock) = %v, %v, want nil, nil", nodes, edges)
	}
}

func TestExpandDetectsLockfilePresence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "uv.lock"), []byte("version = 1\nrevision = 3\nrequires-python = \">=3.9\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nodes, edges, err := (Expander{}).Expand(root)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// A lockfile with zero [[package]] entries has no dependency nodes/edges,
	// but Expand must not treat "uv.lock present, empty" as "not a uv project".
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("Expand(empty uv.lock) = %v, %v, want empty slices", nodes, edges)
	}
}

func TestExpandListsPackagesFromFixtureLockfile(t *testing.T) {
	nodes, _, err := (Expander{}).Expand("testdata/fixtures/root")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var found bool
	for _, n := range nodes {
		if n.Key == "python:idna@3.20" {
			found = true
			if n.Kind != "package" {
				t.Errorf("Kind = %q, want package", n.Kind)
			}
			if !n.Direct {
				t.Error("Direct = false, want true — idna is a direct dependency of roottest")
			}
		}
		if n.Key == "python:roottest@0.1.0" {
			t.Error("the root project itself must not be emitted as a dependency node")
		}
	}
	if !found {
		t.Fatalf("expected node python:idna@3.20, got %v", nodes)
	}
}

func TestMainModulePath(t *testing.T) {
	got, err := MainModulePath("testdata/fixtures/root")
	if err != nil {
		t.Fatalf("MainModulePath: %v", err)
	}
	if got != "roottest" {
		t.Errorf("MainModulePath = %q, want roottest", got)
	}
}

func TestExpandBuildsEdgeFromRootPackage(t *testing.T) {
	_, edges, err := (Expander{}).Expand("testdata/fixtures/root")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	var found bool
	for _, e := range edges {
		if e.From == "python:roottest@0.1.0" && e.To == "python:idna@3.20" {
			found = true
			if e.Resolved != "3.20" {
				t.Errorf("Resolved = %q, want 3.20", e.Resolved)
			}
		}
	}
	if !found {
		t.Fatalf("expected an edge from the root project to python:idna@3.20, got %v", edges)
	}
}

func TestExpandFlagsLocalDirectorySourceAsInternal(t *testing.T) {
	nodes, edges, err := (Expander{}).Expand("testdata/fixtures/root")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	var sawNode bool
	for _, n := range nodes {
		if n.Key == "python:localdep@0.1.0" {
			sawNode = true
		}
	}
	if !sawNode {
		t.Fatalf("expected a node for the locally-sourced package, got %v", nodes)
	}

	var sawInternalEdge bool
	for _, e := range edges {
		if e.To == "python:localdep@0.1.0" {
			if !e.Internal {
				t.Errorf("edge to locally-sourced package has Internal=false, want true: %+v", e)
			}
			sawInternalEdge = true
		}
	}
	if !sawInternalEdge {
		t.Fatalf("expected an edge to python:localdep@0.1.0, got %v", edges)
	}

	// A registry-sourced package must never be flagged internal.
	for _, e := range edges {
		if e.To == "python:idna@3.20" && e.Internal {
			t.Error("edge to a registry-sourced package must not be flagged internal")
		}
	}
}

func TestIsLocalSource(t *testing.T) {
	cases := []struct {
		name   string
		source map[string]string
		want   bool
	}{
		{"registry", map[string]string{"registry": "https://pypi.org/simple"}, false},
		{"git", map[string]string{"git": "https://example.com/x.git"}, false},
		{"directory", map[string]string{"directory": "../dep"}, true},
		{"editable", map[string]string{"editable": "."}, true},
		{"virtual", map[string]string{"virtual": "."}, true},
	}
	for _, c := range cases {
		if got := isLocalSource(c.source); got != c.want {
			t.Errorf("isLocalSource(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
