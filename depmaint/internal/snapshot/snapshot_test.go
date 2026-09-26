package snapshot

import (
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

type fakeExpander struct {
	name      string
	available bool
	nodes     []expander.Node
	edges     []expander.Edge
}

func (f fakeExpander) Name() string    { return f.name }
func (f fakeExpander) Available() bool { return f.available }
func (f fakeExpander) Expand(string) ([]expander.Node, []expander.Edge, error) {
	return f.nodes, f.edges, nil
}

func TestBuildEmptyRegistryIsStable(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s1, nodes1, edges1, err := Build("/repo", nil, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	s2, _, _, err := Build("/repo", nil, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if s1.ID != s2.ID {
		t.Errorf("ID changed across identical empty builds: %q != %q", s1.ID, s2.ID)
	}
	if len(nodes1) != 0 || len(edges1) != 0 || s1.NodeCount != 0 || s1.EdgeCount != 0 {
		t.Errorf("expected empty snapshot, got %d nodes %d edges", len(nodes1), len(edges1))
	}
}

func TestBuildSameInputSameID(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exp := fakeExpander{
		name:      "go",
		available: true,
		nodes: []expander.Node{
			{Key: "go:b@1.0.0", Kind: "package"},
			{Key: "go:a@1.0.0", Kind: "package", Direct: true},
		},
		edges: []expander.Edge{
			{From: "go:a@1.0.0", To: "go:b@1.0.0", Requirement: "^1.0.0", Resolved: "1.0.0"},
		},
	}
	s1, _, _, err := Build("/repo", []expander.Expander{exp}, now)
	if err != nil {
		t.Fatal(err)
	}
	s2, _, _, err := Build("/repo", []expander.Expander{exp}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s1.ID != s2.ID {
		t.Errorf("ID differs for identical node/edge sets: %q != %q", s1.ID, s2.ID)
	}
	if s1.NodeCount != 2 || s1.EdgeCount != 1 {
		t.Errorf("NodeCount=%d EdgeCount=%d, want 2 and 1", s1.NodeCount, s1.EdgeCount)
	}
}

func TestBuildDifferentInputDifferentID(t *testing.T) {
	now := time.Now()
	exp1 := fakeExpander{name: "go", available: true, nodes: []expander.Node{{Key: "go:a@1.0.0", Kind: "package"}}}
	exp2 := fakeExpander{name: "go", available: true, nodes: []expander.Node{{Key: "go:a@2.0.0", Kind: "package"}}}
	s1, _, _, _ := Build("/repo", []expander.Expander{exp1}, now)
	s2, _, _, _ := Build("/repo", []expander.Expander{exp2}, now)
	if s1.ID == s2.ID {
		t.Errorf("expected different IDs for different node sets, both %q", s1.ID)
	}
}

func TestBuildSkipsUnavailableExpanders(t *testing.T) {
	now := time.Now()
	exp := fakeExpander{name: "go", available: false, nodes: []expander.Node{{Key: "go:a@1.0.0"}}}
	s, nodes, _, err := Build("/repo", []expander.Expander{exp}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Errorf("unavailable expander's nodes should be skipped, got %v", nodes)
	}
	if s.Expanders != "" {
		t.Errorf("Expanders = %q, want empty (nothing ran)", s.Expanders)
	}
}

func TestDiff(t *testing.T) {
	prev := []expander.Node{{Key: "a"}, {Key: "b"}}
	curr := []expander.Node{{Key: "b"}, {Key: "c"}}
	d := Diff(prev, curr)
	if len(d.AddedNodes) != 1 || d.AddedNodes[0] != "c" {
		t.Errorf("AddedNodes = %v, want [c]", d.AddedNodes)
	}
	if len(d.RemovedNodes) != 1 || d.RemovedNodes[0] != "a" {
		t.Errorf("RemovedNodes = %v, want [a]", d.RemovedNodes)
	}
}
