package campaign

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeTracker is a hand-written BeadFiler that remembers what it was asked to file.
type fakeTracker struct {
	title, body string
	err         error
}

func (f *fakeTracker) File(_ context.Context, title, body string) (string, error) {
	f.title, f.body = title, body
	return "bead-42", f.err
}

func (l *line) report(id string) string {
	l.t.Helper()
	r, err := l.engine.Report(context.Background(), id)
	if err != nil {
		l.t.Fatalf("Report: %v", err)
	}
	return r
}

func requireAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestReportShowsGoalBudgetBlockersWorkOrderRejectionsAndAttempts(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(4)
	l.record(c.ID, 1, VerdictRejected, "plug 1.1.0 drops the v1 hooks API")

	got := l.report(c.ID)
	requireAll(t, got,
		"goal: upgrade node:lib to 1.2.0",
		"status: running",
		"[#####---------------] 1 of 4 attempts",
		"[constraint] node:plug",
		".depmaint/work/dep_c1-001.md (step 1)",
		"- step 1 by agent-a: plug 1.1.0 drops the v1 hooks API",
		"dep_c1-a001 step 1 by agent-a: rejected — plug 1.1.0 drops the v1 hooks API",
	)
	if again := l.report(c.ID); again != got {
		t.Errorf("report is not deterministic:\n%s\n---\n%s", got, again)
	}
}

func TestEscalateFilesABeadThatReferencesTheReportInsteadOfRestatingIt(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(4)
	tracker := &fakeTracker{}

	r, err := l.engine.Escalate(context.Background(), c.ID, "needs risk acceptance for the hooks API removal", tracker)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Filed || r.Escalation.BeadID != "bead-42" {
		t.Errorf("escalation = %+v, want filed as bead-42", r)
	}
	requireAll(t, tracker.body, "needs risk acceptance for the hooks API removal", ".depmaint/reports/dep_c1.md")
	if strings.Contains(tracker.body, "constraint") {
		t.Errorf("bead restates the report instead of pointing at it:\n%s", tracker.body)
	}
	requireAll(t, r.Report, "## Escalations", "needs risk acceptance for the hooks API removal (bead-42)")
}

func TestEscalateWithoutATrackerRecordsTheReasonAndDoesNotFail(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(4)

	r, err := l.engine.Escalate(context.Background(), c.ID, "budget spent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Filed {
		t.Error("Filed = true with no tracker")
	}
	requireAll(t, l.report(c.ID), "budget spent (no tracker available)")
	requireState(t, l.next(c.ID), NextDispatch) // escalating never fails or stops the campaign
}

func TestEscalateFailsLoudlyWhenTheTrackerRefusesAndRecordsNothing(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(4)

	_, err := l.engine.Escalate(context.Background(), c.ID, "stuck", &fakeTracker{err: errors.New("dolt locked")})
	if err == nil || !strings.Contains(err.Error(), "dolt locked") {
		t.Fatalf("err = %v, want it to carry the tracker failure", err)
	}
	if strings.Contains(l.report(c.ID), "## Escalations") {
		t.Error("a failed filing must not leave an escalation row behind")
	}
}

func TestEscalateRequiresAReason(t *testing.T) {
	l := newLine(t)
	c := l.newPlannedCampaign(4)
	if _, err := l.engine.Escalate(context.Background(), c.ID, "", nil); err == nil {
		t.Fatal("expected an error for an empty reason")
	}
}
