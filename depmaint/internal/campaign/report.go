package campaign

import (
	"context"
	"fmt"
	"strings"
)

// ReportPath is where a campaign's report is written, relative to the repo
// root. It is committed: the report is what humans review and what an
// escalation points at.
func ReportPath(campaignID string) string {
	return fmt.Sprintf(".depmaint/reports/%s.md", campaignID)
}

const budgetBarWidth = 20

// Report renders the human-facing markdown for a campaign. Same stored
// state produces the same bytes: no clock is read, so a committed report
// only changes in a diff when the campaign did.
func (e Engine) Report(ctx context.Context, id string) (string, error) {
	c, err := e.Store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	attempts, err := e.Store.Attempts(ctx, id)
	if err != nil {
		return "", err
	}
	escalations, err := e.Store.Escalations(ctx, id)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Campaign %s\n\n", c.ID)
	fmt.Fprintf(&b, "- goal: upgrade %s to %s\n", c.Package, c.Target)
	fmt.Fprintf(&b, "- status: %s\n", c.Status)
	fmt.Fprintf(&b, "- budget: %s %d of %d attempts; time limit %s\n", budgetBar(c.Spent, c.Budget.MaxAttempts), c.Spent, c.Budget.MaxAttempts, c.Budget.MaxDuration)
	fmt.Fprintf(&b, "- created: %s\n\n", c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"))

	b.WriteString("## Blockers and plan\n\n")
	if c.Plan == nil {
		b.WriteString("No plan yet.\n\n")
	} else {
		b.WriteString("```\n" + c.Plan.Render() + "```\n\n")
		fmt.Fprintf(&b, "%d of %d steps landed.\n\n", c.Completed, c.Completed+len(c.Plan.Steps))
	}

	b.WriteString("## Open work order\n\n")
	if c.Plan != nil && c.Plan.Feasible && c.Status == StatusRunning {
		fmt.Fprintf(&b, "%s (step %d)\n\n", WorkOrderPath(c.ID, c.Completed+1), c.Completed+1)
	} else {
		b.WriteString("None.\n\n")
	}

	b.WriteString("## Options considered and rejected\n\n")
	rejected := 0
	for _, a := range attempts {
		if a.Verdict == VerdictRejected {
			rejected++
			fmt.Fprintf(&b, "- step %d by %s: %s\n", a.Step, a.Executor, a.Reason)
		}
	}
	if rejected == 0 {
		b.WriteString("- none\n")
	}
	b.WriteString("\n")

	b.WriteString("## Attempt log\n\n")
	if len(attempts) == 0 {
		b.WriteString("- none\n")
	}
	for _, a := range attempts {
		fmt.Fprintf(&b, "- %s step %d by %s: %s", a.ID, a.Step, a.Executor, a.Verdict)
		if a.Reason != "" {
			fmt.Fprintf(&b, " — %s", a.Reason)
		}
		if a.PRURL != "" {
			fmt.Fprintf(&b, " (%s)", a.PRURL)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(escalations) > 0 {
		b.WriteString("## Escalations\n\n")
		for _, esc := range escalations {
			bead := esc.BeadID
			if bead == "" {
				bead = "no tracker available"
			}
			fmt.Fprintf(&b, "- %s: %s (%s)\n", esc.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), esc.Reason, bead)
		}
	}
	return b.String(), nil
}

func budgetBar(spent, max int) string {
	filled := min(spent*budgetBarWidth/max, budgetBarWidth)
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", budgetBarWidth-filled) + "]"
}

// BeadFiler files an escalation in the team's tracker. File returns the
// tracker's id for the new item.
type BeadFiler interface {
	File(ctx context.Context, title, body string) (beadID string, err error)
}

// Escalated is the outcome of Escalate.
type Escalated struct {
	Escalation Escalation
	Report     string // the rendered report the escalation points at
	ReportPath string
	// Filed is false when no tracker was available; the escalation is still
	// recorded and the report still rendered, but nobody was notified.
	Filed bool
}

// Escalate records that a campaign needs a human decision and, when filer is
// non-nil, files a bead that references the report instead of restating it.
// The caller writes Report to ReportPath. A nil filer is the "no beads here"
// case: it never fails the campaign.
func (e Engine) Escalate(ctx context.Context, id, reason string, filer BeadFiler) (Escalated, error) {
	if reason == "" {
		return Escalated{}, fmt.Errorf("an escalation needs a reason")
	}
	c, err := e.Store.Get(ctx, id)
	if err != nil {
		return Escalated{}, err
	}
	prior, err := e.Store.Escalations(ctx, id)
	if err != nil {
		return Escalated{}, err
	}
	esc := Escalation{ID: fmt.Sprintf("%s-e%02d", id, len(prior)+1), CampaignID: id, Reason: reason, CreatedAt: e.Now()}
	path := ReportPath(id)
	if filer != nil {
		title := fmt.Sprintf("depmaint %s: upgrade %s to %s needs a decision", id, c.Package, c.Target)
		body := fmt.Sprintf("%s\n\nCampaign report: %s\n", reason, path)
		if esc.BeadID, err = filer.File(ctx, title, body); err != nil {
			return Escalated{}, fmt.Errorf("file bead: %w", err)
		}
	}
	if err := e.Store.AppendEscalation(ctx, esc); err != nil {
		return Escalated{}, err
	}
	report, err := e.Report(ctx, id)
	if err != nil {
		return Escalated{}, err
	}
	return Escalated{Escalation: esc, Report: report, ReportPath: path, Filed: filer != nil}, nil
}
