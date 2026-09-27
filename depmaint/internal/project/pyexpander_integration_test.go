package project

import (
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/pyexpander"
)

// TestPythonExpanderUnionsViaLocalDirectorySource proves the Python
// expander's output round-trips through project derivation correctly: the
// fixture's root project locally references "localdep" (a directory-sourced
// uv.lock entry, i.e. Internal=true), so treating each as its own
// "RootGraph" must still derive ONE project — project derivation is
// ecosystem-agnostic and doesn't care that this data came from Python
// rather than Go.
func TestPythonExpanderUnionsViaLocalDirectorySource(t *testing.T) {
	fixtureRoot, err := filepath.Abs("../pyexpander/testdata/fixtures/root")
	if err != nil {
		t.Fatal(err)
	}

	modPath, err := pyexpander.MainModulePath(fixtureRoot)
	if err != nil {
		t.Fatalf("MainModulePath: %v", err)
	}
	nodes, edges, err := (pyexpander.Expander{}).Expand(fixtureRoot)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least one dependency node from the fixture, got zero")
	}

	// "localdep" is only ever locally-sourced in this fixture, so its own
	// RootGraph carries no further edges — it stands in as a second root
	// the way a sibling Go module would.
	graphs := []RootGraph{
		{Root: "root", ModulePath: "python:" + modPath, Nodes: nodes, Edges: edges},
		{Root: "localdep", ModulePath: "python:localdep"},
	}

	projects := Derive(graphs)
	if len(projects) != 1 {
		t.Fatalf("Derive found %d project(s), want 1 (root locally references localdep): %+v", len(projects), projects)
	}
	if len(projects[0].Roots) != 2 {
		t.Errorf("project has %d roots, want 2: %+v", len(projects[0].Roots), projects[0])
	}
}
