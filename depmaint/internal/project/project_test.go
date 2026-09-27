package project

import (
	"reflect"
	"sort"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

// projectRootSets normalizes Derive's output for comparison: each project
// becomes a sorted slice of its roots, and the outer slice is sorted by
// its first (smallest) root, so test expectations don't depend on Derive's
// internal ordering.
func projectRootSets(projects []Project) [][]string {
	var out [][]string
	for _, p := range projects {
		roots := append([]string(nil), p.Roots...)
		sort.Strings(roots)
		out = append(out, roots)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func TestDeriveSeparatesUnrelatedRoots(t *testing.T) {
	graphs := []RootGraph{
		{Root: "a", ModulePath: "go:example.com/a"},
		{Root: "b", ModulePath: "go:example.com/b"},
	}
	got := projectRootSets(Derive(graphs))
	want := [][]string{{"a"}, {"b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %v, want %v", got, want)
	}
}

func TestDeriveUnionsRootsConnectedByInternalEdge(t *testing.T) {
	graphs := []RootGraph{
		{
			Root:       "a",
			ModulePath: "go:example.com/a",
			Edges: []expander.Edge{
				{From: "go:example.com/a@", To: "go:example.com/b@(local)", Internal: true},
			},
		},
		{Root: "b", ModulePath: "go:example.com/b"},
	}
	got := projectRootSets(Derive(graphs))
	want := [][]string{{"a", "b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %v, want %v", got, want)
	}
}

func TestDeriveIgnoresNonInternalEdgesBetweenRoots(t *testing.T) {
	graphs := []RootGraph{
		{
			Root:       "a",
			ModulePath: "go:example.com/a",
			Edges: []expander.Edge{
				// Same module path as root b, but NOT flagged internal —
				// coincidental external overlap, must not merge.
				{From: "go:example.com/a@", To: "go:example.com/b@v1.0.0", Internal: false},
			},
		},
		{Root: "b", ModulePath: "go:example.com/b"},
	}
	got := projectRootSets(Derive(graphs))
	want := [][]string{{"a"}, {"b"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %v, want %v", got, want)
	}
}

func TestDeriveTransitiveUnion(t *testing.T) {
	// a -> b (internal), b -> c (internal): all three become one project.
	graphs := []RootGraph{
		{
			Root:       "a",
			ModulePath: "go:example.com/a",
			Edges: []expander.Edge{
				{From: "go:example.com/a@", To: "go:example.com/b@(local)", Internal: true},
			},
		},
		{
			Root:       "b",
			ModulePath: "go:example.com/b",
			Edges: []expander.Edge{
				{From: "go:example.com/b@", To: "go:example.com/c@(local)", Internal: true},
			},
		},
		{Root: "c", ModulePath: "go:example.com/c"},
	}
	got := projectRootSets(Derive(graphs))
	want := [][]string{{"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Derive = %v, want %v", got, want)
	}
}
