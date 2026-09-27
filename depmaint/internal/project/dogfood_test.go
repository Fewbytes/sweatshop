package project

import (
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/goexpander"
)

// TestDogfoodSweatshopRootsStaySeparate runs the real Go expander against
// this monorepo's three independent Go modules (agentsh, bughunt, depmaint
// itself) and asserts project derivation correctly keeps them as three
// separate projects — none of them reference each other via a local
// replace directive, so they must NOT be merged, per the spec's rule that
// coincidental repo co-location creates no coordination need.
func TestDogfoodSweatshopRootsStaySeparate(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{"agentsh", "bughunt", "depmaint"}

	var graphs []RootGraph
	for _, r := range roots {
		abs := filepath.Join(repoRoot, r)
		modPath, err := goexpander.MainModulePath(abs)
		if err != nil {
			t.Fatalf("MainModulePath(%s): %v", r, err)
		}
		nodes, edges, err := (goexpander.Expander{}).Expand(abs)
		if err != nil {
			t.Fatalf("Expand(%s): %v", r, err)
		}
		graphs = append(graphs, RootGraph{
			Root:       r,
			ModulePath: "go:" + modPath,
			Nodes:      nodes,
			Edges:      edges,
		})
	}

	projects := Derive(graphs)
	if len(projects) != 3 {
		t.Fatalf("Derive found %d project(s), want 3 (agentsh/bughunt/depmaint are independent modules): %+v", len(projects), projects)
	}
	for _, p := range projects {
		if len(p.Roots) != 1 {
			t.Errorf("project %v has %d roots, want 1 — these modules do not reference each other", p.Roots, len(p.Roots))
		}
	}

	// Sanity check the expanders actually found real dependencies, so this
	// test is exercising the real graph, not three empty modules.
	var totalNodes int
	for _, g := range graphs {
		totalNodes += len(g.Nodes)
	}
	if totalNodes == 0 {
		t.Fatal("expected at least one dependency node across agentsh/bughunt/depmaint, got zero — expander likely broken")
	}
}
