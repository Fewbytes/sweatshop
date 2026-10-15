package project

import (
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/nodeexpander"
)

// TestNodeExpanderUnionsViaFileDependency proves the Node expander's
// Internal flag drives project derivation: the fixture's root references
// "localdep" through a file: dependency, so the two roots form one project.
func TestNodeExpanderUnionsViaFileDependency(t *testing.T) {
	fixtureRoot, err := filepath.Abs("../nodeexpander/testdata/fixtures/root")
	if err != nil {
		t.Fatal(err)
	}
	modPath, err := nodeexpander.MainModulePath(fixtureRoot)
	if err != nil {
		t.Fatalf("MainModulePath: %v", err)
	}
	nodes, edges, err := (nodeexpander.Expander{}).Expand(fixtureRoot)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	graphs := []RootGraph{
		{Root: "root", ModulePath: "node:" + modPath, Nodes: nodes, Edges: edges},
		{Root: "localdep", ModulePath: "node:localdep"},
	}
	projects := Derive(graphs)
	if len(projects) != 1 || len(projects[0].Roots) != 2 {
		t.Fatalf("Derive = %+v, want one project of 2 roots", projects)
	}
}
