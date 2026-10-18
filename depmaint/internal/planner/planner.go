// Package planner turns a project's resolved graph plus published truth into
// an upgrade plan: the blocker set for a target version, and the cheapest
// coordinated set of concurrent upgrades that makes the target feasible.
// Pure functions only — no I/O, no SQL; published truth arrives through
// Registry. Not yet implemented from the spec: preferring sets that unblock
// other open campaigns.
package planner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/project"
)

// Requirement is one dependency constraint a published package version
// declares. Package is a bare "ecosystem:name" identity.
type Requirement struct {
	Package    string
	Constraint string
}

// Registry is published truth: what versions exist and what each requires.
type Registry interface {
	// Versions lists every published version of a bare "ecosystem:name".
	Versions(pkg string) ([]string, error)
	// Requirements lists what pkg@version declares it needs.
	Requirements(pkg, version string) ([]Requirement, error)
}

// Accepts decides whether a version satisfies an ecosystem's requirement
// syntax; SemverAccepts is the semver-flavoured implementation.
type Accepts func(requirement, version string) (bool, error)

// Graph is a project's merged nodes and edges.
type Graph struct {
	Nodes []expander.Node
	Edges []expander.Edge
}

// MergeRoots unions the per-root graphs of one project (see project.Derive),
// dropping duplicate nodes and edges so a shared internal dependency is
// planned once.
func MergeRoots(roots []project.RootGraph) Graph {
	var g Graph
	seenNode := map[string]bool{}
	seenEdge := map[expander.Edge]bool{}
	for _, r := range roots {
		for _, n := range r.Nodes {
			if !seenNode[n.Key] {
				seenNode[n.Key] = true
				g.Nodes = append(g.Nodes, n)
			}
		}
		for _, e := range r.Edges {
			if !seenEdge[e] {
				seenEdge[e] = true
				g.Edges = append(g.Edges, e)
			}
		}
	}
	return g
}

// Upgrade asks to move Package (bare "ecosystem:name") to Target.
type Upgrade struct {
	Package string
	Target  string
}

// BlockerKind says why a node cannot accept the target.
type BlockerKind string

const (
	BlockerConstraint BlockerKind = "constraint" // a package requirement rejects the target
	BlockerToolchain  BlockerKind = "toolchain"  // a toolchain pin or go/requires-python directive
	BlockerEngine     BlockerKind = "engine"     // a node `engines` field
)

// Blocker is a node whose requirement rejects the target version.
type Blocker struct {
	Node   string // node key of the requirer
	Kind   BlockerKind
	Detail string
}

// Step is one upgrade in the ordered plan, issued to an agent as a work order.
type Step struct {
	Package      string
	From, To     string
	Tier         Tier
	Rationale    string
	Verification string
	Rollback     string
}

// Plan is the planner's output. Steps are ordered prerequisites first, the
// requested upgrade last. When Feasible is false Steps is empty and
// Blockers explain why no coordinated set was found.
type Plan struct {
	Upgrade  Upgrade
	Blockers []Blocker
	Feasible bool
	Tier     Tier // largest tier among Steps; meaningful only when Feasible
	Steps    []Step
}

// Make computes the plan for up on g. Tiers are tried patch, minor, major;
// the first tier whose closure is feasible wins. Within a tier each
// violated requirement is repaired by the smallest version bump that
// satisfies it, so sets stay minimal in practice, and the search is
// deterministic. Limitation: a package must appear at one version in g.
func Make(g Graph, up Upgrade, reg Registry, accepts Accepts) (Plan, error) {
	s, err := newSearch(g, up, reg, accepts)
	if err != nil {
		return Plan{}, err
	}
	blockers, err := s.blockers()
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Upgrade: up, Blockers: blockers}
	for _, tier := range []Tier{TierPatch, TierMinor, TierMajor} {
		steps, ok, err := s.closure(tier)
		if err != nil {
			return Plan{}, err
		}
		if ok {
			plan.Feasible, plan.Tier, plan.Steps = true, tier, steps
			return plan, nil
		}
	}
	return plan, nil
}

type search struct {
	graph   Graph
	up      Upgrade
	reg     Registry
	accepts Accepts
	current map[string]string // bare package -> resolved version
	isRoot  map[string]bool   // bare packages with no incoming edge
	nodeOf  map[string]expander.Node
}

func bare(key string) string {
	if i := strings.LastIndex(key, "@"); i >= 0 {
		return key[:i]
	}
	return key
}

func newSearch(g Graph, up Upgrade, reg Registry, accepts Accepts) (*search, error) {
	s := &search{graph: g, up: up, reg: reg, accepts: accepts,
		current: map[string]string{}, isRoot: map[string]bool{}, nodeOf: map[string]expander.Node{}}
	for _, n := range g.Nodes {
		name := bare(n.Key)
		if prev, dup := s.current[name]; dup {
			return nil, fmt.Errorf("%s appears at two versions (%s and %s); the planner needs one per package",
				name, prev, strings.TrimPrefix(n.Key, name+"@"))
		}
		s.current[name] = strings.TrimPrefix(n.Key, name+"@")
		s.isRoot[name] = true
		s.nodeOf[name] = n
	}
	for _, e := range g.Edges {
		delete(s.isRoot, bare(e.To))
	}
	cur, ok := s.current[up.Package]
	if !ok {
		return nil, fmt.Errorf("%s is not in the graph", up.Package)
	}
	if cur == up.Target {
		return nil, fmt.Errorf("%s is already at %s", up.Package, up.Target)
	}
	return s, nil
}

// blockers lists every non-root requirer whose constraint on the target
// package rejects the target version. Roots are skipped: the target step
// itself rewrites the project's own manifest.
func (s *search) blockers() ([]Blocker, error) {
	kind := BlockerConstraint
	if s.nodeOf[s.up.Package].Kind == "runtime" {
		kind = BlockerToolchain
		if strings.HasPrefix(s.up.Package, "node:") {
			kind = BlockerEngine
		}
	}
	var out []Blocker
	for _, e := range s.graph.Edges {
		if bare(e.To) != s.up.Package || s.isRoot[bare(e.From)] {
			continue
		}
		ok, err := s.accepts(e.Requirement, s.up.Target)
		if err != nil {
			return nil, fmt.Errorf("edge %s -> %s: %w", e.From, e.To, err)
		}
		if !ok {
			out = append(out, Blocker{Node: e.From, Kind: kind,
				Detail: fmt.Sprintf("requires %s %s, rejects %s", s.up.Package, e.Requirement, s.up.Target)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

// closure grows a set of upgrades, each no larger than maxTier, until every
// requirement in the project holds with the target applied.
func (s *search) closure(maxTier Tier) ([]Step, bool, error) {
	assigned := map[string]string{s.up.Package: s.up.Target}
	for p, v := range s.current {
		if p != s.up.Package {
			assigned[p] = v
		}
	}
	steps := map[string]Step{}
	for {
		v, found, err := s.firstViolation(assigned, steps)
		if err != nil {
			return nil, false, err
		}
		if !found {
			return s.ordered(steps), true, nil
		}
		repaired := false
		// Move the requirer first, unless it already moved for this plan: then
		// its newer requirements are the fixed point and the dependency yields.
		order := []string{v.requirer, v.req.Package}
		if _, moved := steps[v.requirer]; moved {
			order = []string{v.req.Package, v.requirer}
		}
		for _, pkg := range order {
			if pkg == s.up.Package || s.isRoot[pkg] {
				continue
			}
			to, ok, err := s.smallestFix(pkg, assigned, steps, maxTier)
			if err != nil {
				return nil, false, err
			}
			if !ok {
				continue
			}
			from := s.current[pkg]
			prior, seen := steps[pkg]
			if seen {
				from = prior.From
			}
			a, _ := ParseVersion(from)
			b, _ := ParseVersion(to)
			rationale := fmt.Sprintf("%s@%s requires %s %s, which rejects %s",
				v.requirer, assigned[v.requirer], v.req.Package, v.req.Constraint, assigned[v.req.Package])
			if seen {
				rationale = prior.Rationale
			}
			steps[pkg] = Step{Package: pkg, From: from, To: to, Tier: TierOf(a, b), Rationale: rationale}
			assigned[pkg] = to
			repaired = true
			break
		}
		if !repaired {
			return nil, false, nil
		}
	}
}

// rewrittenByTargetStep reports whether requirer's constraint on dep is the
// project's own manifest entry for the target, which the target step edits.
func (s *search) rewrittenByTargetStep(requirer, dep string) bool {
	return s.isRoot[requirer] && dep == s.up.Package
}

type violation struct {
	requirer string
	req      Requirement
}

// requirementsOf returns what pkg declares at its assigned version: the graph's
// edges while untouched, the registry's published requirements once upgraded.
func (s *search) requirementsOf(pkg, version string, steps map[string]Step) ([]Requirement, error) {
	if _, upgraded := steps[pkg]; upgraded || pkg == s.up.Package {
		return s.reg.Requirements(pkg, version)
	}
	var out []Requirement
	for _, e := range s.graph.Edges {
		if bare(e.From) == pkg {
			out = append(out, Requirement{Package: bare(e.To), Constraint: e.Requirement})
		}
	}
	return out, nil
}

func (s *search) sortedPackages(assigned map[string]string) []string {
	names := make([]string, 0, len(assigned))
	for p := range assigned {
		names = append(names, p)
	}
	sort.Strings(names)
	return names
}

func (s *search) firstViolation(assigned map[string]string, steps map[string]Step) (violation, bool, error) {
	for _, pkg := range s.sortedPackages(assigned) {
		reqs, err := s.requirementsOf(pkg, assigned[pkg], steps)
		if err != nil {
			return violation{}, false, err
		}
		for _, r := range reqs {
			have, tracked := assigned[r.Package]
			if !tracked || s.rewrittenByTargetStep(pkg, r.Package) {
				continue
			}
			ok, err := s.accepts(r.Constraint, have)
			if err != nil {
				return violation{}, false, fmt.Errorf("%s requires %s %q: %w", pkg, r.Package, r.Constraint, err)
			}
			if !ok {
				return violation{requirer: pkg, req: r}, true, nil
			}
		}
	}
	return violation{}, false, nil
}

// smallestFix finds the lowest published version of pkg, newer than its
// assigned one and within maxTier, that is compatible with everything else
// as currently assigned: what others require of pkg (upgraded requirers by
// their published requirements), and what pkg would require of the target.
// Its requirements on other packages may be violated; the closure repairs
// those next, which is how a major bump cascades.
func (s *search) smallestFix(pkg string, assigned map[string]string, steps map[string]Step, maxTier Tier) (string, bool, error) {
	published, err := s.reg.Versions(pkg)
	if err != nil {
		return "", false, err
	}
	have, err := ParseVersion(assigned[pkg])
	if err != nil {
		return "", false, err
	}
	type candidate struct {
		raw string
		v   Version
	}
	var cands []candidate
	for _, raw := range published {
		v, err := ParseVersion(raw)
		if err != nil {
			return "", false, err
		}
		if v.Compare(have) > 0 && TierOf(have, v) <= maxTier {
			cands = append(cands, candidate{raw, v})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].v.Compare(cands[j].v) < 0 })
	for _, c := range cands {
		ok, err := s.compatible(pkg, c.raw, assigned, steps)
		if err != nil {
			return "", false, err
		}
		if ok {
			return c.raw, true, nil
		}
	}
	return "", false, nil
}

func (s *search) compatible(pkg, version string, assigned map[string]string, steps map[string]Step) (bool, error) {
	own, err := s.reg.Requirements(pkg, version)
	if err != nil {
		return false, err
	}
	for _, r := range own {
		if r.Package != s.up.Package {
			continue
		}
		if ok, err := s.accepts(r.Constraint, s.up.Target); err != nil || !ok {
			return false, err
		}
	}
	for _, other := range s.sortedPackages(assigned) {
		reqs, err := s.requirementsOf(other, assigned[other], steps)
		if err != nil {
			return false, err
		}
		for _, r := range reqs {
			if r.Package != pkg {
				continue
			}
			if ok, err := s.accepts(r.Constraint, version); err != nil || !ok {
				return false, err
			}
		}
	}
	return true, nil
}

// ordered emits prerequisites alphabetically, then the requested upgrade,
// each with its verification and rollback text.
func (s *search) ordered(steps map[string]Step) []Step {
	names := make([]string, 0, len(steps))
	for p := range steps {
		names = append(names, p)
	}
	sort.Strings(names)
	out := make([]Step, 0, len(names)+1)
	for _, p := range names {
		out = append(out, s.finish(steps[p]))
	}
	return append(out, s.finish(Step{
		Package: s.up.Package, From: s.current[s.up.Package], To: s.up.Target,
		Tier:      TierOf(mustVersion(s.current[s.up.Package]), mustVersion(s.up.Target)),
		Rationale: "requested upgrade",
	}))
}

func (s *search) finish(st Step) Step {
	st.Verification = fmt.Sprintf("build and tests pass; %s resolves to %s in the next snapshot", st.Package, st.To)
	st.Rollback = fmt.Sprintf("restore %s to %s and re-expand the graph", st.Package, st.From)
	return st
}

// mustVersion parses a version already validated by the registry or graph
// ingest; a failure here means a version slipped past that validation.
func mustVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Render writes the plan as stable text: same plan, same bytes.
func (p Plan) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "upgrade %s to %s\n", p.Upgrade.Package, p.Upgrade.Target)
	fmt.Fprintf(&b, "blockers: %d\n", len(p.Blockers))
	for _, bl := range p.Blockers {
		fmt.Fprintf(&b, "  [%s] %s: %s\n", bl.Kind, bl.Node, bl.Detail)
	}
	if !p.Feasible {
		b.WriteString("feasible: no\n")
		return b.String()
	}
	fmt.Fprintf(&b, "feasible: yes (%s tier)\n", p.Tier)
	for i, st := range p.Steps {
		fmt.Fprintf(&b, "step %d: %s %s -> %s (%s)\n", i+1, st.Package, st.From, st.To, st.Tier)
		fmt.Fprintf(&b, "  why: %s\n  verify: %s\n  rollback: %s\n", st.Rationale, st.Verification, st.Rollback)
	}
	return b.String()
}
