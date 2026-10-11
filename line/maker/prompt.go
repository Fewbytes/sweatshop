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
	var p strings.Builder
	fmt.Fprintf(&p, "You are a Maker station on an assembly line. Your card:\n\n")
	fmt.Fprintf(&p, "ID: %s\nTitle: %s\n\n%s\n\n", b.ID, b.Title, strings.TrimSpace(b.Description))

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
   tracks the card, and bd is not available here.
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
`, indent(trailers, "   "), maxSubmissions, maxSubmissions)
	// Andon cards are fixed by the same maker; both get the maker's guides.
	guide, _ := guidance.For("maker")
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
