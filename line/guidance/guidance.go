// Package guidance holds the code-quality guides the line's code-writing
// roles follow, and hands each role the guides it needs. The markdown files
// next to this one are the only copy; the evidence behind them is in
// research-docs/llm-code-quality.
package guidance

import (
	_ "embed"
	"strings"
)

// Writer is the guide for any agent that writes or changes code or tests.
//
//go:embed writer.md
var Writer string

// Kaizen is the guide for agents that refactor and simplify existing code.
//
//go:embed kaizen.md
var Kaizen string

// Gemba is the guide for the agent that owns acceptance-test meaning and
// field fit.
//
//go:embed gemba.md
var Gemba string

// Testing is the guide to deciding what to test, at which level, and what a
// test must prove. Roles that write or change tests get it.
//
//go:embed testing.md
var Testing string

// byRole lists each constitution role's guides in prompt order. Every role
// has an entry, even an empty one, so adding a role forces a decision here.
var byRole = map[string][]string{
	"maker":   {Writer, Testing},
	"andon":   {Writer},
	"kaizen":  {Writer, Testing, Kaizen},
	"gemba":   {Writer, Testing, Gemba},
	"viceroy": nil, // files cards; writes no code
}

// For returns role's guides as one prompt section, and false for a role the
// line does not know.
func For(role string) (string, bool) {
	docs, ok := byRole[role]
	if !ok {
		return "", false
	}
	return strings.Join(docs, "\n\n"), true
}
