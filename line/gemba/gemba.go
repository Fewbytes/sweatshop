// Package gemba configures the maker runner as a Gemba station: it claims
// only cards labelled Label and briefs the agent with the Gemba mandate and
// the add/fix/retire rule. See design-docs/gemba-agent.md.
package gemba

import "github.com/Fewbytes/sweatshop/line/maker"

// Label marks a card for the Gemba station. Gemba cards are offered to no
// other station, and the Gemba station is offered no other cards.
const Label = "gemba"

// Mandate is the standing orders that follow the card in a Gemba prompt. The
// add/fix/retire rule is the one in line/guidance/gemba.md section 2.
const Mandate = `Mandate: you own the meaning of the acceptance tests, the tests that define
"works" for the outside world. Keep them truthful, sufficient and
non-redundant relative to field reality. You do not build features, stop the
line or simplify structure; if you find work of that kind, say so in the
commit message instead of doing it.

You may change an acceptance test only for one of three reasons, and the
commit message must cite the reason (add, fix or retire) and the evidence:
- add: a field failure or specified behavior lacks a test. The new test must
  fail on the pre-fix commit and pass after; verify both.
- fix: a test asserts the wrong thing. Card evidence is required.
- retire: a test no longer maps to anything used in the field or specified.
  Usage evidence is required.
Any other change to an acceptance test is refused.`

// Prompt is the whole instruction a Gemba run gets for card b.
func Prompt(b maker.Bead) string {
	return maker.RolePrompt("gemba", Mandate, b, "")
}

// Configure makes r a Gemba runner: it is offered only Label cards and its
// agent gets Prompt. Everything else (claiming, running, settling by what
// landed) is the maker runner's.
func Configure(r *maker.Runner) {
	r.Label = Label
	r.PromptFor = Prompt
}
