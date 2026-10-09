// Package andon is the host-side watcher that keeps the line honest: it
// attests every new master, pulls the andon cord when one fails, and clears
// it once a newer master passes. It is a plain loop, not an agent.
package andon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/station"
	"github.com/Fewbytes/sweatshop/line/witness"
)

// API is the slice of the station client the watcher needs.
type API interface {
	Status(ctx context.Context) (station.Status, error)
	AttestLine(ctx context.Context, commit string) (witness.AttestResult, error)
	Pull(ctx context.Context, env attest.Envelope) (guard.Result, error)
	Clear(ctx context.Context, env attest.Envelope) (guard.Result, error)
}

// Cards files work items for andon events.
type Cards interface {
	// Raise files a P0 card for an andon event and returns its ID.
	Raise(ctx context.Context, event string, ev guard.AndonEvent) (string, error)
}

// Watcher observes the line's master.
type Watcher struct {
	API      API
	Cards    Cards
	Interval time.Duration // default 30s
	Log      *log.Logger
	last     string // last master attested
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Log != nil {
		w.Log.Printf(format, args...)
	}
}

// Step makes one observation. last advances only once a master's attestation
// completed, so any error means the same master is looked at again.
func (w *Watcher) Step(ctx context.Context) error {
	st, err := w.API.Status(ctx)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if st.Master == "" || st.Master == w.last {
		return nil
	}
	res, err := w.API.AttestLine(ctx, st.Master)
	if err != nil {
		return fmt.Errorf("attest %s: %w", st.Master, err)
	}
	if res.Envelope == nil {
		return fmt.Errorf("attest %s: no envelope returned", st.Master)
	}
	w.last = st.Master

	if !res.Passed {
		w.logf("master %s fails its checks; pulling the andon cord", st.Master)
		pr, err := w.API.Pull(ctx, *res.Envelope)
		if err != nil {
			w.last = ""
			return fmt.Errorf("pull: %w", err)
		}
		switch {
		case pr.Status == guard.Stopped:
			w.logf("andon pulled, event %s", pr.Event)
			id, err := w.Cards.Raise(ctx, pr.Event, eventFromResult(res))
			if err != nil {
				return fmt.Errorf("raise card for event %s: %w", pr.Event, err)
			}
			w.logf("filed card %s for event %s", id, pr.Event)
		case pr.Reason == guard.ReasonAlreadyStopped:
			w.logf("andon already pulled (event %s)", pr.Event)
		case pr.Reason == guard.ReasonStaleEvidence:
			// Master moved while the checks ran; look at the new one next step.
			w.last = ""
			w.logf("pull was stale; will retry")
		default:
			w.last = ""
			return fmt.Errorf("pull rejected: %s %s", pr.Reason, pr.Detail)
		}
		return nil
	}

	if st.Event == "" {
		w.logf("master %s passes", st.Master)
		return nil
	}
	cr, err := w.API.Clear(ctx, *res.Envelope)
	if err != nil {
		w.last = ""
		return fmt.Errorf("clear: %w", err)
	}
	switch {
	case cr.Status == guard.Cleared:
		w.logf("master %s passes; andon cleared (event %s)", st.Master, st.Event)
	case cr.Reason == guard.ReasonNotStopped:
		w.logf("andon already cleared")
	default:
		// Not retried blindly: clearing the failing commit itself is refused,
		// and a newer master will be attested on its own.
		if cr.Reason == guard.ReasonStaleEvidence && st.Andon != nil && st.Andon.Commit == st.Master {
			w.logf("master %s is the failing commit; waiting for a newer master", st.Master)
			return nil
		}
		w.last = ""
		return fmt.Errorf("clear rejected: %s %s", cr.Reason, cr.Detail)
	}
	return nil
}

// eventFromResult reconstructs what the card needs from the failing
// attestation (the status was read before the pull, so it has no event yet).
func eventFromResult(res witness.AttestResult) guard.AndonEvent {
	ev := guard.AndonEvent{Commit: res.Commit}
	for _, c := range res.Statement.Predicate.Checks {
		if c.ExitCode != 0 {
			ev.Check, ev.ExitCode, ev.EvidenceID = c.Name, c.ExitCode, c.EvidenceID
			break
		}
	}
	return ev
}

// Run steps every Interval until ctx is done. Step errors are logged: the
// station may be restarting, and the next step simply retries.
func (w *Watcher) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := w.Step(ctx); err != nil && ctx.Err() == nil {
			w.logf("step: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// BeadCards files andon cards with the bd CLI.
type BeadCards struct{ Dir string } // runs `bd create` in Dir

// Raise runs `bd create` and returns the new issue's ID. The description
// carries `event: <id>` on its own line; the fixer's prompt parses it.
func (b BeadCards) Raise(ctx context.Context, event string, ev guard.AndonEvent) (string, error) {
	short := ev.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	desc := strings.Join([]string{
		"The andon cord was pulled: the line's master fails a required check.",
		"",
		"event: " + event,
		"commit: " + ev.Commit,
		"check: " + ev.Check,
		fmt.Sprintf("exit code: %d", ev.ExitCode),
		"evidence: " + ev.EvidenceID,
		"",
		"Fix or revert. The fix commit must carry the trailer `" + guard.FixTrailer + ": " + event + "`.",
	}, "\n")
	cmd := exec.CommandContext(ctx, "bd", "create",
		fmt.Sprintf("andon: master %s fails %s", short, ev.Check),
		"-t", "bug", "-p", "0", "-l", "line", "-l", "andon", "-d", desc, "--json")
	cmd.Dir = b.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bd create: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		return "", fmt.Errorf("bd create: parse output: %w", err)
	}
	if created.ID == "" {
		return "", errors.New("bd create: no id in output")
	}
	return created.ID, nil
}
