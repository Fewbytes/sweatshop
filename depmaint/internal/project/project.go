// Package project derives projects from per-root dependency graphs: roots
// connected through internal (repo-local) edges are one project; roots that
// merely share an external dependency are not (spec: "coincidental external
// overlap creates no coordination need"). Ecosystem-agnostic — operates
// purely on expander.Node/Edge keys and each root's own declared identity.
package project

import (
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

// RootGraph is one root's expanded dependency graph plus its own identity.
type RootGraph struct {
	Root       string // caller-defined identifier for this root (e.g. a path)
	ModulePath string // this root's own "ecosystem:name" identity, e.g. "go:example.com/a"
	Nodes      []expander.Node
	Edges      []expander.Edge
}

// Project is a set of roots that are connected through internal edges.
type Project struct {
	Roots []string
}

// bareKey strips a node/edge key's "@version" suffix, leaving "ecosystem:name".
func bareKey(key string) string {
	if i := strings.LastIndex(key, "@"); i >= 0 {
		return key[:i]
	}
	return key
}

// Derive unions roots connected by at least one Internal edge whose
// endpoint matches another root's own ModulePath (version-stripped, since a
// local replace target's version is often absent or a placeholder).
func Derive(graphs []RootGraph) []Project {
	parent := make(map[string]string, len(graphs))
	for _, g := range graphs {
		parent[g.Root] = g.Root
	}
	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	identity := make(map[string]string, len(graphs)) // bare module identity -> root
	for _, g := range graphs {
		identity[bareKey(g.ModulePath)] = g.Root
	}

	for _, g := range graphs {
		for _, e := range g.Edges {
			if !e.Internal {
				continue
			}
			if otherRoot, ok := identity[bareKey(e.To)]; ok && otherRoot != g.Root {
				union(g.Root, otherRoot)
			}
			if otherRoot, ok := identity[bareKey(e.From)]; ok && otherRoot != g.Root {
				union(g.Root, otherRoot)
			}
		}
	}

	groups := make(map[string][]string)
	for _, g := range graphs {
		root := find(g.Root)
		groups[root] = append(groups[root], g.Root)
	}
	projects := make([]Project, 0, len(groups))
	for _, roots := range groups {
		projects = append(projects, Project{Roots: roots})
	}
	return projects
}
