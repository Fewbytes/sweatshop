// Package kaizen runs a Kaizen station: a host-side loop that, on a schedule
// and only when master has moved, lets an agent in a station container make
// one measured, behavior-preserving improvement and land it through the merge
// guard. See design-docs/kaizen-agent.md ("Station v1").
package kaizen

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guidance"
)

// maxSubmissions bounds how often the agent may call `sweatshop line merge`
// in one run, so a change it cannot land costs a bounded amount of work.
const maxSubmissions = 3

// reportFields are the labels of the change report template in
// line/guidance/kaizen.md, section 6. The template is the only copy of the
// format; TestReportFieldsAreInTheTemplate fails if the two diverge.
var reportFields = []string{"Target", "Category", "Hotspot", "Change", "Signals", "Safety", "Behavior", "Follow-ups"}

// signalDelta matches a Signals line that names a signal with a before and an
// after value: `Signals: <signal>: <before> → <after>`. Values that start
// with `<` are the template's placeholders left unfilled.
var signalDelta = regexp.MustCompile(`(?m)^Signals:[ \t]+\S.*:[ \t]*[^\s<][^\n]*→[ \t]*[^\s<]`)

// CheckReport fails unless message carries the full change report, with a
// before → after value for at least one signal. The merge client calls it so
// that every Kaizen commit that lands carries its own evidence in master's
// history, where the realized delta can be audited later.
func CheckReport(message string) error {
	var missing []string
	for _, f := range reportFields {
		if !regexp.MustCompile(`(?m)^` + f + `:`).MatchString(message) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("commit message lacks the change report fields %s: copy the template from section 6 of the kaizen guide into the message body", strings.Join(missing, ", "))
	}
	if !signalDelta.MatchString(message) {
		return fmt.Errorf("the Signals line must read `Signals: <signal>: <before> → <after>`")
	}
	return nil
}

// CheckCommitReport applies CheckReport to rev's commit message.
func CheckCommitReport(ctx context.Context, repo gitx.Repo, rev string) error {
	msg, err := repo.Git(ctx, "log", "-1", "--format=%B", rev, "--")
	if err != nil {
		return err
	}
	return CheckReport(msg)
}

// Prompt is the whole instruction one Kaizen run gets. runID is the value of
// the `Bead` trailer the commit must carry, which is how the runner sees that
// the run landed.
func Prompt(runID string) string {
	guide, _ := guidance.For("kaizen")
	var p strings.Builder
	fmt.Fprintf(&p, `You are a Kaizen station on an assembly line. Your run: %[1]s

Mandate: make ONE small, behavior-preserving improvement to the codebase in
/work and land it. If nothing measurable is worth improving, change nothing
and finish with RESULT: gave-up nothing worth improving. Making work to look
busy is a failure.

Line protocol:

1. Work in /work, a clone of the line's master (origin is /line/remote.git).
   Read CLAUDE.md for the project's conventions, but skip its beads/bd
   instructions: bd is not available here. You can read the line's cards with
   `+"`sweatshop line cards list [--status S] [--label L]`"+` and `+"`... show <id>`"+`; open cards
   show what Makers are changing now. Do not touch code they are changing.
2. Choose the target. Measure, do not guess. Signals you must measure with
   tools in /work, using the table in the guide below:
   - hotspot score = churn x complexity: `+"`git log --since=90.days --name-only`"+`
     for churn, function and file size or linter output for complexity;
   - co-change without a static dependency (hidden coupling), duplicated
     knowledge, import cycles (`+"`go list`"+`), dead symbols;
   - test runtime and total `+"`just ci`"+` duration.
   Pick the highest-ranked finding that one refactoring can fix. Skip a
   target that has no tests catching a behavior change unless you add
   characterization tests as their own commit first.
3. Record the baseline value of the signal you will move BEFORE you edit.
4. Apply one refactoring, then run `+"`just ci`"+` and make it pass. Re-measure the
   same signal the same way.
5. Commit. The message body must hold the filled-in change report below, one
   field per line, with a real before → after value on the Signals line (the
   merge client refuses the commit otherwise). The trailer is the last
   paragraph, with nothing after it (no Co-Authored-By paragraph):

   Bead: %[1]s

   Report template:

   Target:    <path / symbol>
   Category:  P<n> <name>
   Hotspot:   churn=<n> complexity=<n>
   Change:    <one refactoring, one line>
   Signals:   <signal>: <before> → <after>
   Safety:    <tests run>, <characterization tests added?>
   Behavior:  unchanged (tests: <pass count>), public API unchanged
   Follow-ups: <issue ids or none>

6. Submit with `+"`sweatshop line merge`"+`. It exits 0 only when the commit merged.
   - base_moved: `+"`git fetch origin && git rebase origin/master`"+`, re-run
     `+"`just ci`"+`, re-measure, and submit again.
   - checks_failed: read the evidence, fix, commit, submit again.
   Submit at most %[2]d times in total.
7. You cannot write cards. Anything you found but did not fix, put in the
   Follow-ups field in words; the viceroy reads landed commits.
8. Never edit files outside the repository in /work. Never try to reach the
   line by any other means than `+"`sweatshop line`"+`.
9. Stop when the commit merged or after %[2]d failed submissions. End your
   output with exactly one summary line:
   RESULT: merged <sha>
   or
   RESULT: gave-up <reason>

Code quality guide (applies to every change you make):

%[3]s
`, runID, maxSubmissions, guide)
	return p.String()
}
