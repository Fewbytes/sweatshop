package purity

import (
	"fmt"
	"slices"
	"strings"
)

// markdownRowLimit bounds each table so the report stays inside the kaizen
// report size limit on a large tree; the JSON output is complete.
const markdownRowLimit = 25

// Markdown renders the report for `sweatshop line kaizen report`: totals, the
// mixed packages with the most direct effects, and every layering violation.
func (r Report) Markdown() string {
	nonDeterministic := 0
	for _, f := range r.Functions {
		if f.Tag == TagNonDeterministic {
			nonDeterministic++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Purity analysis\n\n%d functions, %d non-deterministic (%d%%), %d packages, %d layering violations.\n",
		len(r.Functions), nonDeterministic, percent(nonDeterministic, len(r.Functions)), len(r.Packages), len(r.Violations))

	mixed := []PackageSummary{}
	for _, p := range r.Packages {
		if p.Mixed {
			mixed = append(mixed, p)
		}
	}
	slices.SortStableFunc(mixed, func(a, b PackageSummary) int { return b.DirectEffects - a.DirectEffects })
	fmt.Fprintf(&b, "\n## Packages mixing pure logic with effects (%d)\n\n", len(mixed))
	if len(mixed) > 0 {
		b.WriteString("| package | direct effects | deterministic | sources |\n|---|---|---|---|\n")
	}
	for _, p := range mixed[:min(len(mixed), markdownRowLimit)] {
		sources := make([]string, len(p.Sources))
		for i, s := range p.Sources {
			sources[i] = string(s)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s |\n", p.Path, p.DirectEffects, p.Deterministic, strings.Join(sources, ", "))
	}

	fmt.Fprintf(&b, "\n## Logic packages depending on adapters (%d)\n\n", len(r.Violations))
	for _, v := range r.Violations[:min(len(r.Violations), markdownRowLimit)] {
		fmt.Fprintf(&b, "- %s imports %s", v.Package, v.Imports)
		if len(v.Calls) > 0 {
			fmt.Fprintf(&b, ", calling %s", strings.Join(v.Calls, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func percent(part, whole int) int {
	if whole == 0 {
		return 0
	}
	return part * 100 / whole
}
