package analyzer

import (
	"fmt"
	"sort"
	"strings"
)

// BaselineComparison compares the current invocation's log analysis against prior
// occurrences of templates for the same command.
func BaselineComparison(current *LogAnalysis, priorTemplateCounts map[string]int, priorRunCount int) {
	if current == nil {
		return
	}

	var novelCount, rareCount, knownCount int
	var novelErrors, novelStackTraces int

	for i := range current.Templates {
		t := &current.Templates[i]
		prior, exists := priorTemplateCounts[t.ID]
		t.PriorCount = prior
		if !exists || prior == 0 {
			t.Novel = true
			novelCount++
			if t.IsStackTrace {
				novelStackTraces++
			} else if t.Level == "ERROR" || t.Level == "FATAL" {
				novelErrors++
			}
		} else if priorRunCount > 0 && float64(prior)/float64(priorRunCount) <= 0.2 {
			rareCount++
		} else {
			knownCount++
		}
	}

	// Re-rank templates: novel first (errors/stacktraces first), rare next, frequent noise last
	sort.SliceStable(current.Templates, func(i, j int) bool {
		ti, tj := current.Templates[i], current.Templates[j]
		if ti.Novel != tj.Novel {
			return ti.Novel
		}
		if ti.Novel {
			// Prioritize novel stacktraces and errors
			priI := templatePriority(ti)
			priJ := templatePriority(tj)
			if priI != priJ {
				return priI > priJ
			}
		}
		// If both known/rare, sort by rarity (lowest prior count first)
		if ti.PriorCount != tj.PriorCount {
			return ti.PriorCount < tj.PriorCount
		}
		return ti.Count > tj.Count
	})

	if priorRunCount > 0 {
		var summaryParts []string
		if novelCount > 0 {
			novelDesc := fmt.Sprintf("%d novel template", novelCount)
			if novelCount > 1 {
				novelDesc += "s"
			}
			var flags []string
			if novelErrors > 0 {
				flags = append(flags, fmt.Sprintf("%d error", novelErrors))
			}
			if novelStackTraces > 0 {
				flags = append(flags, fmt.Sprintf("%d stack trace", novelStackTraces))
			}
			if len(flags) > 0 {
				novelDesc += fmt.Sprintf(" (%s)", strings.Join(flags, ", "))
			}
			summaryParts = append(summaryParts, novelDesc)
		}
		if rareCount > 0 {
			summaryParts = append(summaryParts, fmt.Sprintf("%d rare", rareCount))
		}
		summaryParts = append(summaryParts, fmt.Sprintf("%d known templates across %d prior run(s)", knownCount, priorRunCount))
		current.Summary = strings.Join(summaryParts, "; ")
	}
}

func templatePriority(t LogTemplate) int {
	if t.IsStackTrace {
		return 3
	}
	if t.Level == "FATAL" || t.Level == "ERROR" {
		return 2
	}
	if t.Level == "WARN" {
		return 1
	}
	return 0
}
