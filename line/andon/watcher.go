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
	// Raise returns the live (open or in_progress) card for an andon event,
	// filing a P0 card when there is none. master is the line's current
	// master.
	Raise(ctx context.Context, event string, ev guard.AndonEvent, master string) (string, error)
}

// Watcher observes the line's master.
type Watcher struct {
	API      API
	Cards    Cards
	Interval time.Duration // default 30s
	Log      *log.Logger
	last     string            // last master attested
	carded   map[string]string // andon event -> card last seen, for logging
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
	// Carding: whenever a step ends with the line stopped, the event must have
	// a live (open or in_progress) card. Raise is idempotent, so this runs every
	// such step: a failed Raise, a restart, a card closed by a fix that did not
	// clear the line, all end with a card. It runs only once the current master
	// has a verdict, so a fix that is about to clear the line (its card already
	// closed by the maker) does not get a needless fresh card first.
	stoppedEvent := func() guard.AndonEvent {
		if st.Andon != nil {
			return *st.Andon
		}
		return guard.AndonEvent{Commit: st.Master}
	}
	if st.Master == "" {
		return nil
	}
	if st.Master == w.last {
		if st.Event != "" {
			return w.ensureCard(ctx, st.Event, stoppedEvent(), st.Master)
		}
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
			return w.ensureCard(ctx, pr.Event, eventFromResult(res), st.Master)
		case pr.Reason == guard.ReasonAlreadyStopped:
			// Typically a fix landed but master still fails (its card may be
			// closed by now), or a restart met an existing stop.
			w.logf("andon already pulled (event %s)", pr.Event)
			event, ev := pr.Event, eventFromResult(res)
			if st.Event != "" && (event == "" || event == st.Event) {
				event, ev = st.Event, stoppedEvent()
			}
			if event == "" {
				return nil
			}
			return w.ensureCard(ctx, event, ev, st.Master)
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
			return w.ensureCard(ctx, st.Event, stoppedEvent(), st.Master)
		}
		w.last = ""
		return errors.Join(fmt.Errorf("clear rejected: %s %s", cr.Reason, cr.Detail),
			w.ensureCard(ctx, st.Event, stoppedEvent(), st.Master))
	}
	return nil
}

// ensureCard makes sure event has a live card; Raise is idempotent. master is
// the line's current master, which the card names when it differs from the
// event's commit (a fix landed but master still fails).
func (w *Watcher) ensureCard(ctx context.Context, event string, ev guard.AndonEvent, master string) error {
	id, err := w.Cards.Raise(ctx, event, ev, master)
	if err != nil {
		return fmt.Errorf("raise card for event %s: %w", event, err)
	}
	if w.carded == nil {
		w.carded = map[string]string{}
	}
	if w.carded[event] != id {
		w.carded[event] = id
		w.logf("card %s for event %s", id, event)
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
	if ev.Check == "" {
		ev.Check = "unknown check"
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

// existing returns the ID of a live (open or in_progress) andon card already
// naming event, so a restarted watcher, a retry after a lost reply, or a card
// a fixer has claimed never yields a duplicate. A closed card does not count:
// if the line is still stopped, its fix did not work.
func (b BeadCards) existing(ctx context.Context, event string) (string, error) {
	cmd := exec.CommandContext(ctx, "bd", "list", "--label", "andon", "--status", "open,in_progress", "--limit", "0", "--json")
	cmd.Dir = b.Dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bd list: %w", err)
	}
	var issues []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		return "", fmt.Errorf("bd list: parse output: %w", err)
	}
	for _, is := range issues {
		for _, line := range strings.Split(is.Description, "\n") {
			if line == "event: "+event {
				return is.ID, nil
			}
		}
	}
	return "", nil
}

// Raise runs `bd create` and returns the new issue's ID. The description
// carries `event: <id>` on its own line; the fixer's prompt parses it.
func (b BeadCards) Raise(ctx context.Context, event string, ev guard.AndonEvent, master string) (string, error) {
	if id, err := b.existing(ctx, event); err != nil {
		return "", err
	} else if id != "" {
		return id, nil
	}
	if master == "" {
		master = ev.Commit
	}
	short := master
	if len(short) > 12 {
		short = short[:12]
	}
	if ev.Check == "" {
		ev.Check = "unknown check"
	}
	lead := "The andon cord was pulled: the line's master fails a required check."
	if master != ev.Commit {
		lead = "A fix landed but master still fails: the line is still stopped on this event."
	}
	desc := strings.Join([]string{
		lead,
		"",
		"event: " + event,
		"commit: " + ev.Commit,
		"master: " + master,
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
