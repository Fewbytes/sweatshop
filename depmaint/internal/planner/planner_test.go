package planner

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/project"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden from current output")

// fakeRegistry is hand-written published truth: package -> version -> requirements.
type fakeRegistry map[string]map[string][]Requirement

func (f fakeRegistry) Versions(pkg string) ([]string, error) {
	var out []string
	for v := range f[pkg] {
		out = append(out, v)
	}
	return out, nil
}

func (f fakeRegistry) Requirements(pkg, version string) ([]Requirement, error) {
	return f[pkg][version], nil
}

func pkgNode(key string, direct bool) expander.Node {
	return expander.Node{Key: key, Kind: "package", Direct: direct}
}

func edge(from, to, requirement string) expander.Edge {
	return expander.Edge{From: from, To: to, Requirement: requirement}
}

// appGraph is the hand-computed fixture: app (root) uses lib, plug and util;
// plug@1.0.0 pins lib to the 1.0 line.
func appGraph() Graph {
	return Graph{
		Nodes: []expander.Node{
			pkgNode("npm:app@1.0.0", false), pkgNode("npm:lib@1.0.0", true),
			pkgNode("npm:plug@1.0.0", true), pkgNode("npm:util@1.0.0", true),
		},
		Edges: []expander.Edge{
			edge("npm:app@1.0.0", "npm:lib@1.0.0", "^1.0.0"),
			edge("npm:app@1.0.0", "npm:plug@1.0.0", ">=1.0.0"),
			edge("npm:app@1.0.0", "npm:util@1.0.0", ">=1.0.0"),
			edge("npm:plug@1.0.0", "npm:lib@1.0.0", "~1.0.0"),
		},
	}
}

func appRegistry() fakeRegistry {
	return fakeRegistry{
		"npm:lib": {"1.0.0": nil, "1.0.1": nil, "1.2.0": nil, "2.0.0": nil},
		"npm:plug": {
			"1.0.0": {{"npm:lib", "~1.0.0"}},
			"1.0.1": {{"npm:lib", "~1.0.0"}},
			"1.1.0": {{"npm:lib", "^1.0.0"}},
			"2.0.0": {{"npm:lib", "^2.0.0"}, {"npm:util", "^2.0.0"}},
		},
		"npm:util": {"1.0.0": nil, "2.0.0": nil},
	}
}

func makePlan(t *testing.T, g Graph, reg fakeRegistry, target string) Plan {
	t.Helper()
	p, err := Make(g, Upgrade{Package: "npm:lib", Target: target}, reg, SemverAccepts)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	return p
}

func requireGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(want) != got {
		t.Errorf("plan differs from %s\n--- want\n%s--- got\n%s", path, want, got)
	}
}

func stepSummary(p Plan) string {
	var parts []string
	for _, s := range p.Steps {
		parts = append(parts, s.Package+" "+s.From+"->"+s.To+" "+s.Tier.String())
	}
	return strings.Join(parts, "; ")
}

func TestUnblockedPatchUpgradeIsOneStep(t *testing.T) {
	p := makePlan(t, appGraph(), appRegistry(), "1.0.1")
	if len(p.Blockers) != 0 || !p.Feasible || p.Tier != TierPatch {
		t.Fatalf("want no blockers, feasible, patch tier; got %+v", p)
	}
	if got, want := stepSummary(p), "npm:lib 1.0.0->1.0.1 patch"; got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
}

func TestMinorBlockerIsClearedByMinorCoordinatedSet(t *testing.T) {
	p := makePlan(t, appGraph(), appRegistry(), "1.2.0")
	if len(p.Blockers) != 1 || p.Blockers[0].Node != "npm:plug@1.0.0" || p.Blockers[0].Kind != BlockerConstraint {
		t.Fatalf("blockers = %+v, want one constraint blocker on npm:plug@1.0.0", p.Blockers)
	}
	if p.Tier != TierMinor {
		t.Errorf("tier = %s, want minor (patch plug 1.0.1 still pins ~1.0.0)", p.Tier)
	}
	if got, want := stepSummary(p), "npm:plug 1.0.0->1.1.0 minor; npm:lib 1.0.0->1.2.0 minor"; got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
	requireGolden(t, "minor-coordinated", p.Render())
}

func TestMajorUpgradeCascadesThroughNewRequirements(t *testing.T) {
	p := makePlan(t, appGraph(), appRegistry(), "2.0.0")
	if p.Tier != TierMajor {
		t.Fatalf("tier = %s, want major", p.Tier)
	}
	want := "npm:plug 1.0.0->2.0.0 major; npm:util 1.0.0->2.0.0 major; npm:lib 1.0.0->2.0.0 major"
	if got := stepSummary(p); got != want {
		t.Errorf("steps = %q, want %q (plug 2 needs util 2, which no step otherwise touches)", got, want)
	}
	requireGolden(t, "major-cascade", p.Render())
}

func TestUpgradeIsInfeasibleWhenNoPublishedVersionUnblocks(t *testing.T) {
	reg := appRegistry()
	delete(reg["npm:plug"], "2.0.0")
	p := makePlan(t, appGraph(), reg, "2.0.0")
	if p.Feasible || len(p.Steps) != 0 || len(p.Blockers) != 1 {
		t.Fatalf("want infeasible with one blocker and no steps; got %+v", p)
	}
	requireGolden(t, "infeasible", p.Render())
}

func TestRootManifestPinOnAnotherPackageStopsTheCascade(t *testing.T) {
	g := appGraph()
	if p := makePlan(t, g, appRegistry(), "2.0.0"); !p.Feasible {
		t.Fatalf("app accepts util >=1, so util 2 is reachable: %+v", p)
	}
	g.Edges = append(g.Edges, edge("npm:app@1.0.0", "npm:util@1.0.0", "<2.0.0"))
	if p := makePlan(t, g, appRegistry(), "2.0.0"); p.Feasible {
		t.Errorf("root pins util below 2, so plug 2 is unreachable; got steps %s", stepSummary(p))
	}
}

func TestPlanIsByteIdenticalOnRepeat(t *testing.T) {
	a := makePlan(t, appGraph(), appRegistry(), "2.0.0").Render()
	for i := 0; i < 20; i++ {
		if b := makePlan(t, appGraph(), appRegistry(), "2.0.0").Render(); a != b {
			t.Fatalf("run %d differs:\n%s\nvs\n%s", i, a, b)
		}
	}
}

func TestRuntimeBlockerKindFollowsEcosystem(t *testing.T) {
	cases := map[string]BlockerKind{"node:node": BlockerEngine, "go:go": BlockerToolchain}
	for pkg, kind := range cases {
		g := Graph{
			Nodes: []expander.Node{pkgNode("npm:app@1.0.0", false), pkgNode("npm:old@1.0.0", true),
				{Key: pkg + "@18.0.0", Kind: "runtime", Direct: true}},
			Edges: []expander.Edge{
				edge("npm:app@1.0.0", "npm:old@1.0.0", "^1.0.0"),
				edge("npm:old@1.0.0", pkg+"@18.0.0", "<20.0.0"),
			},
		}
		p, err := Make(g, Upgrade{Package: pkg, Target: "20.0.0"}, fakeRegistry{}, SemverAccepts)
		if err != nil {
			t.Fatalf("%s: %v", pkg, err)
		}
		if len(p.Blockers) != 1 || p.Blockers[0].Kind != kind {
			t.Errorf("%s: blockers = %+v, want one of kind %s", pkg, p.Blockers, kind)
		}
	}
}

func TestMakeRejectsBadInput(t *testing.T) {
	g := appGraph()
	bad := g
	bad.Nodes = append(append([]expander.Node{}, g.Nodes...), pkgNode("npm:lib@1.1.0", true))
	cases := map[string]struct {
		g  Graph
		up Upgrade
	}{
		"unknown package":       {g, Upgrade{"npm:nope", "1.0.0"}},
		"already at target":     {g, Upgrade{"npm:lib", "1.0.0"}},
		"two versions in graph": {bad, Upgrade{"npm:lib", "2.0.0"}},
	}
	for name, c := range cases {
		if _, err := Make(c.g, c.up, appRegistry(), SemverAccepts); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	g.Edges[3].Requirement = "~banana"
	if _, err := Make(g, Upgrade{"npm:lib", "1.2.0"}, appRegistry(), SemverAccepts); err == nil {
		t.Error("malformed requirement: want error, got silent accept")
	}
}

func TestMergeRootsPlansInternallySharedDependencyOnce(t *testing.T) {
	shared := pkgNode("npm:shared@1.0.0", true)
	roots := []project.RootGraph{
		{Root: "a", Nodes: []expander.Node{pkgNode("npm:a@1.0.0", false), shared},
			Edges: []expander.Edge{edge("npm:a@1.0.0", "npm:shared@1.0.0", "^1.0.0")}},
		{Root: "b", Nodes: []expander.Node{pkgNode("npm:b@1.0.0", false), shared},
			Edges: []expander.Edge{edge("npm:b@1.0.0", "npm:shared@1.0.0", "^1.0.0"), edge("npm:a@1.0.0", "npm:shared@1.0.0", "^1.0.0")}},
	}
	g := MergeRoots(roots)
	if len(g.Nodes) != 3 || len(g.Edges) != 2 {
		t.Fatalf("merged %d nodes %d edges, want 3 and 2", len(g.Nodes), len(g.Edges))
	}
	if _, err := Make(g, Upgrade{"npm:shared", "1.1.0"}, fakeRegistry{}, SemverAccepts); err != nil {
		t.Errorf("merged graph should plan: %v", err)
	}
}

func TestSemverAccepts(t *testing.T) {
	cases := []struct {
		req, ver string
		want     bool
	}{
		{"^1.2.0", "1.9.0", true}, {"^1.2.0", "2.0.0", false}, {"^1.2.0", "1.1.0", false},
		{"^0.2.0", "0.2.9", true}, {"^0.2.0", "0.3.0", false},
		{"~1.2.0", "1.2.7", true}, {"~1.2.0", "1.3.0", false},
		{">=1.0.0 <2.0.0", "1.5.0", true}, {">=1.0.0, <2.0.0", "2.0.0", false},
		{"1.0.0", "1.0.0", true}, {"1.0.0", "1.0.1", false}, {"*", "9.9.9", true},
	}
	for _, c := range cases {
		got, err := SemverAccepts(c.req, c.ver)
		if err != nil || got != c.want {
			t.Errorf("SemverAccepts(%q, %q) = %v, %v; want %v", c.req, c.ver, got, err, c.want)
		}
	}
}
