package maker

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Fewbytes/sweatshop/line/guidance"
)

// maxSubmissions bounds how often the agent may call `sweatshop line merge`
// for one card, so a card it cannot finish costs a bounded amount of work.
const maxSubmissions = 3

// AndonEvent returns the event ID an andon card names: the value of the
// description line `event: <id>` the andon watcher writes. Cards without the
// `andon` label have none, so an ordinary card cannot smuggle in a fix
// trailer that would let it merge while the line is stopped.
func AndonEvent(b Bead) string {
	if !slices.Contains(b.Labels, "andon") {
		return ""
	}
	for _, line := range strings.Split(b.Description, "\n") {
		if ev, found := strings.CutPrefix(strings.TrimSpace(line), "event: "); found && ev != "" {
			return strings.TrimSpace(ev)
		}
	}
	return ""
}

// Prompt is the whole instruction the station's agent gets: the card and the
// line protocol. The agent's only route to the line is `sweatshop line`.
func Prompt(b Bead, andonEvent string) string {
	return RolePrompt("maker", "", b, andonEvent)
}

// RolePrompt is Prompt for the station of role: the same protocol, with the
// needs:<role> question label and the guides of guidance.For(role). mandate,
// if not empty, follows the card as the role's standing orders. Stations that
// work cards through this runner (gemba) differ from a Maker only in these.
func RolePrompt(role, mandate string, b Bead, andonEvent string) string {
	var p strings.Builder
	fmt.Fprintf(&p, "You are a %s station on an assembly line. Your card:\n\n", strings.ToUpper(role[:1])+role[1:])
	fmt.Fprintf(&p, "ID: %s\nTitle: %s\n\n%s\n\n", b.ID, b.Title, strings.TrimSpace(b.Description))

	if mandate != "" {
		fmt.Fprintf(&p, "%s\n\n", strings.TrimSpace(mandate))
	}

	trailers := "Bead: " + b.ID
	if andonEvent != "" {
		trailers += "\nAndon-Event: " + andonEvent
		fmt.Fprintf(&p, "This is an andon card: the line is stopped because master fails a check. "+
			"While it is stopped only fix commits carrying `Andon-Event: %s` can merge. "+
			"Fix master (or revert the breaking change) and nothing else.\n\n", andonEvent)
	}

	fmt.Fprintf(&p, `Line protocol:

1. Work in /work, a clone of the line's master (origin is /line/remote.git).
   Keep the change small and focused on this card, and follow the code
   quality guide at the end of this prompt. Read CLAUDE.md for the
   project's conventions, but skip its beads/bd instructions: the host
   tracks the card, and bd is not available here. You may read the line's
   cards for more context: `+"`sweatshop line cards show <id>`"+` (description,
   comments, dependencies, parent) and
   `+"`sweatshop line cards list [--parent ID] [--label L] [--status S]`"+`.
   Read the cards this one depends on or descends from when it refers to
   them or leaves something unclear. Reading is optional.
   Before starting, run `+"`sweatshop line cards list --label needs:%s`"+`: cards
   labelled so are questions addressed to you. Answer each you can by
   commenting (`+"`sweatshop line cards comment <id>`"+`, text on stdin); you may
   decline, and the asker decides how to proceed. Otherwise you cannot write
   cards.
2. Run `+"`just ci`"+` and make it pass before submitting.
3. Commit with these trailers as the last paragraph of the commit message,
   with nothing after them (no Co-Authored-By paragraph):

%s

4. Submit with `+"`sweatshop line merge`"+` (it checks HEAD and asks the merge guard
   to land it). It exits 0 only when the commit merged.
   - On status base_moved: `+"`git fetch origin && git rebase origin/master`"+`,
     re-run `+"`just ci`"+`, and submit again.
   - On checks_failed (or any failing check): read the evidence it prints,
     fix the problem, commit, and submit again.
   Submit at most %d times in total.
5. Never edit files outside the repository in /work. Never try to reach the
   line by any other means than `+"`sweatshop line`"+` (no pushing, no other hosts).
6. Stop when the commit merged or after %d failed submissions. End your output
   with exactly one summary line:
   RESULT: merged <sha>
   or
   RESULT: gave-up <reason>
`, role, indent(trailers, "   "), maxSubmissions, maxSubmissions)
	guide, _ := guidance.For(role)
	fmt.Fprintf(&p, "\nCode quality guide (applies to every change you make):\n\n%s\n", guide)
	return p.String()
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
