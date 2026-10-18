package campaign

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Fewbytes/sweatshop/depmaint/internal/planner"
)

// WorkOrderPath is where a campaign step's work order is written, relative
// to the repo root. It is committed so a team can review what agents were
// asked to do.
func WorkOrderPath(campaignID string, step int) string {
	return fmt.Sprintf(".depmaint/work/%s-%03d.md", campaignID, step)
}

var manifestByEcosystem = map[string]string{
	"go":     "go.mod",
	"node":   "package.json",
	"python": "pyproject.toml or requirements*.txt",
	"jvm":    "pom.xml or build.gradle",
}

// renderWorkOrder writes the self-contained prompt for the campaign's live
// step. Same campaign, plan and graph produce the same bytes: nothing here
// reads a clock, and every list is sorted.
func renderWorkOrder(c Campaign, g planner.Graph, attempts []Attempt) string {
	step := c.Plan.Steps[0]
	n := c.Completed + 1
	ecosystem, _, _ := strings.Cut(step.Package, ":")
	manifest := manifestByEcosystem[ecosystem]
	if manifest == "" {
		manifest = "the manifest that declares it"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Work order %s step %d of %d\n\n", c.ID, n, c.Completed+len(c.Plan.Steps))

	b.WriteString("## Goal and step\n\n")
	fmt.Fprintf(&b, "- campaign: %s — upgrade %s to %s\n", c.ID, c.Package, c.Target)
	fmt.Fprintf(&b, "- this step: move %s from %s to %s (%s)\n", step.Package, step.From, step.To, step.Tier)
	fmt.Fprintf(&b, "- attempt budget: %d of %d spent\n\n", c.Spent, c.Budget.MaxAttempts)

	b.WriteString("## Why\n\n")
	fmt.Fprintf(&b, "%s\n\n", step.Rationale)
	if len(c.Plan.Blockers) > 0 {
		b.WriteString("Blockers on the requested upgrade:\n\n")
		for _, bl := range c.Plan.Blockers {
			fmt.Fprintf(&b, "- [%s] %s: %s\n", bl.Kind, bl.Node, bl.Detail)
		}
		b.WriteString("\n")
	}

	b.WriteString("## The change\n\n")
	fmt.Fprintf(&b, "Edit %s under each of: %s\n\n", manifest, strings.Join(c.Roots, ", "))
	fmt.Fprintf(&b, "Expected movement: %s %s -> %s\n", step.Package, step.From, step.To)
	if step.Tier == planner.TierMajor {
		b.WriteString("Break signal: major-version bump; read the release notes for removed or changed APIs before editing.\n")
	}
	b.WriteString("\n")

	b.WriteString("## Relevant subgraph\n\n")
	var lines []string
	for _, e := range g.Edges {
		if barePackage(e.From) == step.Package || barePackage(e.To) == step.Package {
			lines = append(lines, fmt.Sprintf("- %s -> %s (requires %s)", e.From, e.To, e.Requirement))
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		b.WriteString("- no edges touch this package\n")
	}
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")

	b.WriteString("## Prior attempts\n\n")
	if len(attempts) == 0 {
		b.WriteString("- none\n")
	}
	for _, a := range attempts {
		fmt.Fprintf(&b, "- step %d by %s: %s", a.Step, a.Executor, a.Verdict)
		if a.Reason != "" {
			fmt.Fprintf(&b, " — %s", a.Reason)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## Verification\n\n")
	fmt.Fprintf(&b, "- %s\n", step.Verification)
	b.WriteString("- `bughunt scan` reports no new findings\n")
	fmt.Fprintf(&b, "- expected snapshot delta: remove %s@%s, add %s@%s\n", step.Package, step.From, step.Package, step.To)
	fmt.Fprintf(&b, "- rollback if it cannot be made green: %s\n\n", step.Rollback)

	b.WriteString("## Report back\n\n")
	fmt.Fprintf(&b, "depmaint campaign record %s --step %d --verdict <succeeded|rejected|failed> \\\n", c.ID, n)
	b.WriteString("  --reason \"...\" [--pr <url>] [--executor <name>]\n")
	return b.String()
}

// barePackage strips "@version" from a node key.
func barePackage(key string) string {
	if i := strings.LastIndex(key, "@"); i > 0 {
		return key[:i]
	}
	return key
}
