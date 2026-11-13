// Package purity tags the functions of Go modules deterministic or
// non-deterministic by taint analysis and reports where a package's
// dependencies break the ports-and-adapters layering. See
// design-docs/purity-analysis.md for what counts as a source and how taint
// travels.
package purity

import (
	"slices"
	"strings"
)

// Tag says whether a function's result depends only on its arguments.
type Tag string

const (
	TagDeterministic    Tag = "deterministic"
	TagNonDeterministic Tag = "non-deterministic"
)

// FunctionTag is the verdict for one function with the evidence for it.
type FunctionTag struct {
	ID  string `json:"id"`
	Tag Tag    `json:"tag"`
	// Source is the effect kind at the end of Chain; empty when deterministic.
	Source Source `json:"source,omitempty"`
	// Chain runs from this function through its callees to the function that
	// touches Source itself (this function alone when it does); empty when
	// deterministic.
	Chain []string `json:"chain,omitempty"`
	// Via is the call or variable that introduces Source in the last chain
	// function, such as "time.Now" or "global requestCount".
	Via string `json:"via,omitempty"`
}

// PackageRole separates packages that touch effects themselves from those that
// do not.
type PackageRole string

const (
	// RoleAdapter packages contain at least one function that touches a source directly.
	RoleAdapter PackageRole = "adapter"
	// RoleLogic packages contain no direct source: any non-determinism is inherited.
	RoleLogic PackageRole = "logic"
)

// PackageSummary counts a package's functions by tag.
type PackageSummary struct {
	Path             string      `json:"path"`
	Role             PackageRole `json:"role"`
	Functions        int         `json:"functions"`
	Deterministic    int         `json:"deterministic"`
	NonDeterministic int         `json:"non_deterministic"`
	DirectEffects    int         `json:"direct_effects"`
	Sources          []Source    `json:"sources,omitempty"`
	// Mixed packages hold both functions that touch sources directly and
	// deterministic ones: the logic is a candidate to move out of the adapter.
	Mixed bool `json:"mixed"`
}

// Violation is a logic package depending on an adapter package.
type Violation struct {
	Package string `json:"package"`
	Imports string `json:"imports"`
	// Calls are the non-deterministic functions of Imports that Package calls;
	// empty when it imports the adapter without calling into its effects.
	Calls []string `json:"calls,omitempty"`
}

// Report is the result of analysing a tree.
type Report struct {
	Functions  []FunctionTag    `json:"functions"`
	Packages   []PackageSummary `json:"packages"`
	Violations []Violation      `json:"violations"`
}

// Analyze tags every function of the Go modules under root (non-test files)
// and reports mixed packages and layering violations. It is read-only.
func Analyze(root string) (Report, error) {
	units, err := scanPackages(root)
	if err != nil {
		return Report{}, err
	}
	facts := mergeFacts(units)
	resolveGlobals(units, facts)
	edges := callEdges(units, facts)
	tags := propagate(facts, edges)
	tagOf := map[string]Tag{}
	for _, t := range tags {
		tagOf[t.ID] = t.Tag
	}
	report := Report{Functions: tags}
	report.Packages = summarize(units, facts, tagOf)
	report.Violations = findViolations(units, report.Packages, edges, tagOf, facts)
	return report, nil
}

// mergeFacts indexes functions by ID, merging per-platform duplicates.
func mergeFacts(units []*packageUnit) map[string]*funcFacts {
	facts := map[string]*funcFacts{}
	for _, unit := range units {
		for _, f := range unit.funcs {
			prev, dup := facts[f.id]
			if !dup {
				facts[f.id] = f
				continue
			}
			prev.callIDs = append(prev.callIDs, f.callIDs...)
			prev.qcalls = append(prev.qcalls, f.qcalls...)
			prev.assignedGlobals = append(prev.assignedGlobals, f.assignedGlobals...)
			prev.readGlobals = append(prev.readGlobals, f.readGlobals...)
			for src, evidence := range f.direct {
				if _, seen := prev.direct[src]; !seen {
					prev.direct[src] = evidence
				}
			}
		}
	}
	return facts
}

// resolveGlobals marks functions touching a package variable that some
// function of the package assigns. Variables never assigned after
// initialisation (sentinel errors, lookup tables) are constants in effect.
func resolveGlobals(units []*packageUnit, facts map[string]*funcFacts) {
	for _, unit := range units {
		mutable := map[string]bool{}
		for _, f := range unit.funcs {
			for _, name := range f.assignedGlobals {
				mutable[name] = true
			}
		}
		for _, f := range unit.funcs {
			for _, name := range f.readGlobals {
				if mutable[name] {
					if _, seen := f.direct[SourceGlobal]; !seen {
						f.direct[SourceGlobal] = "global " + name
					}
				}
			}
		}
	}
}

// callEdges maps each function to the analysed functions it calls, sorted.
func callEdges(units []*packageUnit, facts map[string]*funcFacts) map[string][]string {
	edges := map[string][]string{}
	for id, f := range facts {
		callees := map[string]bool{}
		for _, callee := range f.callIDs {
			if _, known := facts[callee]; known && callee != id {
				callees[callee] = true
			}
		}
		for _, q := range f.qcalls {
			callee := q.importPath + "." + q.name
			if _, known := facts[callee]; known && callee != id {
				callees[callee] = true
			}
		}
		for callee := range callees {
			edges[id] = append(edges[id], callee)
		}
		slices.Sort(edges[id])
	}
	return edges
}

// propagate spreads direct sources to callers breadth-first, so each tainted
// function's chain is a shortest path to a function that touches a source.
func propagate(facts map[string]*funcFacts, edges map[string][]string) []FunctionTag {
	ids := make([]string, 0, len(facts))
	for id := range facts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	callers := map[string][]string{}
	for _, id := range ids {
		for _, callee := range edges[id] {
			callers[callee] = append(callers[callee], id)
		}
	}
	next := map[string]string{} // tainted function -> callee one step closer to the source
	var queue []string
	for _, id := range ids {
		if len(facts[id].direct) > 0 {
			next[id] = ""
			queue = append(queue, id)
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, caller := range callers[queue[head]] {
			if _, tainted := next[caller]; !tainted {
				next[caller] = queue[head]
				queue = append(queue, caller)
			}
		}
	}
	tags := make([]FunctionTag, 0, len(ids))
	for _, id := range ids {
		if _, tainted := next[id]; !tainted {
			tags = append(tags, FunctionTag{ID: id, Tag: TagDeterministic})
			continue
		}
		chain := []string{id}
		for end := id; next[end] != ""; {
			end = next[end]
			chain = append(chain, end)
		}
		source := firstSource(facts[chain[len(chain)-1]].direct)
		tags = append(tags, FunctionTag{
			ID: id, Tag: TagNonDeterministic, Source: source, Chain: chain,
			Via: facts[chain[len(chain)-1]].direct[source],
		})
	}
	return tags
}

// firstSource is the alphabetically first source, so the report is stable.
func firstSource(direct map[Source]string) Source {
	var first Source
	for src := range direct {
		if first == "" || src < first {
			first = src
		}
	}
	return first
}

func summarize(units []*packageUnit, facts map[string]*funcFacts, tagOf map[string]Tag) []PackageSummary {
	var out []PackageSummary
	for _, unit := range units {
		sum := PackageSummary{Path: unit.importPath, Role: RoleLogic}
		seen := map[string]bool{}
		sources := map[Source]bool{}
		for _, f := range unit.funcs {
			if seen[f.id] {
				continue
			}
			seen[f.id] = true
			sum.Functions++
			if tagOf[f.id] == TagDeterministic {
				sum.Deterministic++
			} else {
				sum.NonDeterministic++
			}
			if len(facts[f.id].direct) > 0 {
				sum.DirectEffects++
				sum.Role = RoleAdapter
				for src := range facts[f.id].direct {
					sources[src] = true
				}
			}
		}
		for src := range sources {
			sum.Sources = append(sum.Sources, src)
		}
		slices.Sort(sum.Sources)
		sum.Mixed = sum.DirectEffects > 0 && sum.Deterministic > 0
		out = append(out, sum)
	}
	slices.SortFunc(out, func(a, b PackageSummary) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// findViolations flags each logic package that imports an adapter package of
// the analysed modules. main packages are composition roots and may wire
// adapters in.
func findViolations(units []*packageUnit, summaries []PackageSummary, edges map[string][]string, tagOf map[string]Tag, facts map[string]*funcFacts) []Violation {
	role := map[string]PackageRole{}
	for _, s := range summaries {
		role[s.Path] = s.Role
	}
	violations := []Violation{}
	for _, unit := range units {
		if unit.name == "main" || role[unit.importPath] != RoleLogic {
			continue
		}
		for _, imp := range unit.imports {
			if role[imp] != RoleAdapter {
				continue
			}
			v := Violation{Package: unit.importPath, Imports: imp}
			called := map[string]bool{}
			for _, f := range unit.funcs {
				for _, callee := range edges[f.id] {
					if facts[callee].pkg == imp && !called[callee] && tagOf[callee] == TagNonDeterministic {
						called[callee] = true
					}
				}
			}
			for callee := range called {
				v.Calls = append(v.Calls, callee)
			}
			slices.Sort(v.Calls)
			violations = append(violations, v)
		}
	}
	return violations
}
