// Package maker runs a Maker station: a host-side loop that takes a ready
// bead, lets an agent in a station container work it through the station
// API, and settles the bead by what actually landed on the line's master.
package maker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/station"
)

type Bead struct {
	ID, Title, Description string
	Priority               int
	Labels                 []string
}

type Beads interface {
	// Ready lists ready beads with label, excluding GaveUpLabel.
	Ready(ctx context.Context, label string) ([]Bead, error)
	Claim(ctx context.Context, id, agent string) error
	Close(ctx context.Context, id, reason string) error
	Release(ctx context.Context, id, note string) error
	// CommentCount is how many comments the card has, so the runner can tell
	// whether a run left a failure report.
	CommentCount(ctx context.Context, id string) (int, error)
	// GiveUp comments reason on the bead and labels it GaveUpLabel so no
	// maker picks it again. With escalateToKing it also labels it needs:king:
	// nobody else can unstick it.
	GiveUp(ctx context.Context, id, reason string, escalateToKing bool) error
	// Claimed lists the in_progress cards assigned to agent.
	Claimed(ctx context.Context, agent string) ([]Bead, error)
}

// GaveUpLabel marks a bead the maker failed to land MaxAttempts times.
const GaveUpLabel = station.LabelGaveUp

// DefaultMaxAttempts caps the runs one bead gets before the maker gives up.
const DefaultMaxAttempts = 3

// Heartbeater is the slice of the station client the maker reports liveness
// through.
type Heartbeater interface {
	Heartbeat(ctx context.Context, hb station.Heartbeat) error
}

type Engine interface {
	// Run starts the station container and waits for it; returns exit code.
	Run(ctx context.Context, spec ContainerSpec) (int, error)
}

type Mount struct {
	Host, Container string
	ReadOnly        bool
}

type ContainerSpec struct {
	Image, Name string
	Env         map[string]string
	Mounts      []Mount
	Args        []string
	// Resource ceilings in engine syntax (--memory, --pids-limit, --cpus);
	// zero values are omitted.
	Memory, CPUs string
	Pids         int
	// Output, if not nil, also receives the container's stdout and stderr.
	Output io.Writer
}

type Runner struct {
	// Memory, Pids and CPUs are the station container's resource ceilings.
	Memory, CPUs string
	Pids         int

	Line    linedir.Line
	Station linedir.Station
	Beads   Beads
	// Label is the label a card needs to be offered; default station.LabelLine.
	Label string
	// PromptFor builds the agent's instruction for a card; default Prompt.
	PromptFor func(Bead) string
	Engine    Engine
	URL       string // as seen from inside the container; default http://host.containers.internal:<port>
	// Token is the station's bearer token; empty reads Station.TokenFile.
	Token   string
	Creds   map[string]string // model credentials passed through to the container
	Timeout time.Duration     // bounds one container run; default DefaultTimeout
	// MaxAttempts caps the runs one bead gets (default DefaultMaxAttempts).
	// Attempts are counted in this process; runs cut short by the engine or
	// by interruption do not count against the bead.
	MaxAttempts int
	// StopPoll is how often a run of a non-andon card checks whether the cord
	// was pulled (default DefaultStopPoll).
	StopPoll time.Duration
	Log      *log.Logger
	// Heartbeats tells the line this maker is alive; nil sends none. Commit is
	// the maker binary's commit, reported with each heartbeat.
	Heartbeats Heartbeater
	Commit     string

	// SettleRetryWait is the pause between tries of a settle step (default
	// DefaultSettleRetryWait).
	SettleRetryWait time.Duration

	attempts map[string]int
	// lastLanded is whether the card Once last worked landed; Loop retries a
	// card that did not only after its idle wait.
	lastLanded bool
}

// DefaultTimeout bounds a Maker run so a stuck agent cannot hold a card (and
// the station) forever.
const DefaultTimeout = 2 * time.Hour

// DefaultSettleRetryWait is the pause between tries of a failed settle step.
const DefaultSettleRetryWait = 5 * time.Second

// settleTries is how often a settle step (read master, close, release) is
// tried before the card is escalated to an operator.
const settleTries = 3

// DefaultStopPoll is how often a running maker looks for a pulled cord.
const DefaultStopPoll = 5 * time.Second

// errCordPulled is the cancel cause of a run cut short because the andon
// cord was pulled while it worked a non-andon card.
var errCordPulled = errors.New("andon cord pulled")

// EngineErrorExit is the exit code docker and podman use when they could not
// run the container at all, as opposed to the container's own exit code.
const EngineErrorExit = 125

// credentialVars are the host variables a station container may inherit.
var credentialVars = []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// HostCreds collects the model credentials set in the host environment.
func HostCreds(lookup func(string) (string, bool)) map[string]string {
	creds := map[string]string{}
	for _, k := range credentialVars {
		if v, set := lookup(k); set && v != "" {
			creds[k] = v
		}
	}
	return creds
}

// Pick chooses the next card: andon cards first (the line is stopped until
// one lands), then the lowest priority number, then ID for a stable order.
// Beads labelled GaveUpLabel are never picked, and while the line is stopped
// only andon cards are: anything else could only be refused (line_stopped).
func Pick(beads []Bead, stopped bool) (Bead, bool) {
	sorted := slices.DeleteFunc(slices.Clone(beads), func(b Bead) bool {
		return slices.Contains(b.Labels, GaveUpLabel) || (stopped && !slices.Contains(b.Labels, "andon"))
	})
	if len(sorted) == 0 {
		return Bead{}, false
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		ai, aj := slices.Contains(sorted[i].Labels, "andon"), slices.Contains(sorted[j].Labels, "andon")
		if ai != aj {
			return ai
		}
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted[0], true
}

// Landed reports whether a commit added to remote's master since pre (the
// master recorded before the run) carries the trailer `Bead: <id>`. The line
// is the source of truth: the agent's own report of success is never
// trusted. Trailers are agent-authored, so only commits this run could have
// added count; an older one (another card's commit naming this bead, or an
// earlier partial land of a reopened bead) does not.
func Landed(ctx context.Context, remote gitx.Repo, beadID, pre string) (bool, error) {
	if !fullHex.MatchString(pre) {
		return false, fmt.Errorf("bad pre-run master %q", pre)
	}
	added, err := remote.CommitsSince(ctx, pre, "refs/heads/master")
	if err != nil {
		return false, err
	}
	for _, commit := range added {
		beads, err := remote.Trailer(ctx, commit, "Bead")
		if err != nil {
			return false, err
		}
		if slices.Contains(beads, beadID) {
			return true, nil
		}
	}
	return false, nil
}

// SignalCause is the context cause to cancel with when a signal ends the
// maker; its text is the signal's name, which the runner logs.
func SignalCause(sig os.Signal) error { return errors.New(sig.String()) }

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Printf(format, args...)
	}
}

var (
	unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	fullHex         = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// stopped reports whether the line's andon cord is pulled.
func (r *Runner) stopped(ctx context.Context) (bool, error) {
	return gitx.Repo{Dir: r.Line.Remote}.RefExists(ctx, guard.StopRef)
}

func (r *Runner) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return DefaultMaxAttempts
}

// beat sends one heartbeat; a line that cannot be reached must not stop the
// maker, so a failure is only logged.
func (r *Runner) beat(ctx context.Context, hb station.Heartbeat) {
	if r.Heartbeats == nil {
		return
	}
	hb.Commit = r.Commit
	if err := r.Heartbeats.Heartbeat(ctx, hb); err != nil && ctx.Err() == nil {
		r.logf("heartbeat: %v", err)
	}
}

// beatWhileWorking heartbeats "working card" every station.MakerPollInterval
// until the returned stop is called: a run lasts far longer than the line's
// silence limit.
func (r *Runner) beatWhileWorking(ctx context.Context, card string) (stop func()) {
	return BeatEveryPoll(ctx, func(ctx context.Context) { r.beat(ctx, station.Heartbeat{State: station.StateWorking, Card: card}) })
}

// BeatEveryPoll calls beat at once and then every station.MakerPollInterval
// until the returned stop is called, which waits for the last beat to finish.
func BeatEveryPoll(ctx context.Context, beat func(context.Context)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			beat(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(station.MakerPollInterval):
			}
		}
	}()
	return func() { cancel(); <-done }
}

// settleStep runs fn up to settleTries times, pausing between tries.
func (r *Runner) settleStep(ctx context.Context, id, what string, fn func() error) error {
	wait := r.SettleRetryWait
	if wait <= 0 {
		wait = DefaultSettleRetryWait
	}
	var err error
	for try := 1; try <= settleTries; try++ {
		if err = fn(); err == nil {
			return nil
		}
		r.logf("%s: %s (try %d/%d): %v", id, what, try, settleTries, err)
		if try < settleTries {
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
		}
	}
	return fmt.Errorf("%s: %s: %w", id, what, err)
}

// escalateUnsettled labels a card whose settle failed for good, so an
// operator sees it; otherwise it would stay claimed with nobody working it.
// The claim stays: the run may have landed, and releasing would invite a
// duplicate.
func (r *Runner) escalateUnsettled(ctx context.Context, id string, cause error) error {
	r.logf("%s: Once returned without settling: %v", id, cause)
	if err := r.Beads.GiveUp(ctx, id, "could not settle after the run: "+cause.Error(), true); err != nil {
		return errors.Join(cause, fmt.Errorf("%s: escalate unsettled card: %w", id, err))
	}
	return cause
}

// releaseStranded releases cards still assigned to this station's agent: it
// works one card at a time, so a claim it holds between runs belongs to a run
// that never settled (process death, failed settle).
func (r *Runner) releaseStranded(ctx context.Context) {
	stranded, err := r.Beads.Claimed(ctx, r.Station.AgentID)
	if err != nil {
		r.logf("list claimed cards: %v", err)
		return
	}
	for _, b := range stranded {
		if slices.Contains(b.Labels, GaveUpLabel) {
			continue // already escalated to an operator
		}
		r.logf("%s: releasing stranded claim", b.ID)
		if err := r.Beads.Release(ctx, b.ID, "maker restarted; run did not settle"); err != nil {
			r.logf("%s: release stranded claim: %v", b.ID, err)
		}
	}
}

// cancelWhenCordPulled cancels run with errCordPulled once the cord is pulled,
// polling every StopPoll until ctx ends. Check failures are logged and retried.
func (r *Runner) cancelWhenCordPulled(ctx context.Context, cancel context.CancelCauseFunc) {
	poll := r.StopPoll
	if poll <= 0 {
		poll = DefaultStopPoll
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
		pulled, err := r.stopped(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.logf("read andon state: %v", err)
			}
			continue
		}
		if pulled {
			cancel(errCordPulled)
			return
		}
	}
}

// idleReason says why Pick found nothing among the ready cards.
func idleReason(ready []Bead, stopped bool, label string) string {
	if len(ready) == 0 {
		return fmt.Sprintf("no ready cards labelled %s", label)
	}
	if stopped && !slices.ContainsFunc(ready, func(b Bead) bool { return slices.Contains(b.Labels, "andon") }) {
		return fmt.Sprintf("andon cord pulled and none of %d ready cards is an andon card", len(ready))
	}
	return fmt.Sprintf("all %d ready cards are labelled %s", len(ready), GaveUpLabel)
}

// Once works one card: pick → claim → run → settle. It reports whether there
// was a card to work.
func (r *Runner) Once(ctx context.Context) (bool, error) {
	r.beat(ctx, station.Heartbeat{State: station.StateIdle})
	r.releaseStranded(ctx)
	label := r.Label
	if label == "" {
		label = station.LabelLine
	}
	ready, err := r.Beads.Ready(ctx, label)
	if err != nil {
		return false, fmt.Errorf("ready beads: %w", err)
	}
	stopped, err := r.stopped(ctx)
	if err != nil {
		return false, fmt.Errorf("read andon state: %w", err)
	}
	b, found := Pick(ready, stopped)
	r.lastLanded = false
	if !found {
		r.beat(ctx, station.Heartbeat{State: station.StateIdle, IdleReason: idleReason(ready, stopped, label)})
		return false, nil
	}
	token := r.Token
	if token == "" {
		var err error
		if token, err = r.Station.Token(); err != nil {
			return false, fmt.Errorf("station %s token: %w", r.Station.Name, err)
		}
	}
	// Only commits added after this point can show that the run landed.
	pre, err := gitx.Repo{Dir: r.Line.Remote}.Ref(ctx, "refs/heads/master")
	if err != nil {
		return false, fmt.Errorf("read line master: %w", err)
	}
	if err := r.Beads.Claim(ctx, b.ID, r.Station.AgentID); err != nil {
		return false, fmt.Errorf("claim %s: %w", b.ID, err)
	}
	r.logf("claimed %s: %s", b.ID, b.Title)

	url := r.URL
	if url == "" {
		url = fmt.Sprintf("http://host.containers.internal:%d", r.Line.Port)
	}
	env := map[string]string{}
	for k, v := range r.Creds {
		env[k] = v
	}
	env["SWEATSHOP_LINE_URL"] = url
	env["SWEATSHOP_LINE_TOKEN"] = token
	env["SWEATSHOP_BEAD"] = b.ID
	env["SWEATSHOP_AGENT_ID"] = r.Station.AgentID
	env["SWEATSHOP_PROMPT"] = Prompt(b, AndonEvent(b))
	if r.PromptFor != nil {
		env["SWEATSHOP_PROMPT"] = r.PromptFor(b)
	}
	if ev := AndonEvent(b); ev != "" {
		env["SWEATSHOP_ANDON_EVENT"] = ev
	}
	commentsBefore, commentsBeforeErr := r.Beads.CommentCount(ctx, b.ID)
	tail := &tailBuffer{max: failureTailBytes}
	spec := ContainerSpec{
		Output: tail,
		Image:  r.Line.Image,
		Name:   unsafeNameChars.ReplaceAllString("sweatshop-"+r.Station.Name+"-"+b.ID, "_"),
		Env:    env,
		Mounts: []Mount{{Host: r.Line.Remote, Container: "/line/remote.git", ReadOnly: true}},
		Memory: r.Memory, CPUs: r.CPUs, Pids: r.Pids,
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, timeout)
	runCtx, cancelCause := context.WithCancelCause(timeoutCtx)
	cancelRun := func() { cancelCause(nil); cancelTimeout() }
	if !slices.Contains(b.Labels, "andon") {
		// An andon card is the one thing a pulled cord asks makers to work.
		go r.cancelWhenCordPulled(runCtx, cancelCause)
	}
	stopBeating := r.beatWhileWorking(ctx, b.ID)
	code, runErr := r.Engine.Run(runCtx, spec)
	stopBeating()
	cordPulled := errors.Is(context.Cause(runCtx), errCordPulled)
	timedOut := errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancelRun()
	r.logf("%s: container exited %d (err %v)", b.ID, code, runErr)

	note := fmt.Sprintf("maker run ended without landing (exit %d)", code)
	counts := true // whether this run counts against the bead's attempts
	switch {
	case timedOut:
		note = fmt.Sprintf("maker run timed out after %s without landing (exit %d)", timeout, code)
	case ctx.Err() != nil:
		r.logf("exiting on %v: releasing %s", context.Cause(ctx), b.ID)
		note = fmt.Sprintf("maker run interrupted by %v without landing (exit %d)", context.Cause(ctx), code)
		counts = false
	case cordPulled:
		note = fmt.Sprintf("maker run stopped: andon cord pulled (exit %d)", code)
		counts = false
	case runErr == nil && code == EngineErrorExit:
		// docker/podman exit 125 when they could not run the container at
		// all; that is the station's fault, not the agent's.
		runErr = fmt.Errorf("%s: container engine failed (exit %d)", b.ID, code)
		note = fmt.Sprintf("maker run ended without landing: container engine error (exit %d)", code)
		counts = false
	case runErr != nil:
		note = fmt.Sprintf("maker run ended without landing: container engine error: %v", runErr)
		counts = false
	}
	giveUp := false
	if counts {
		if r.attempts == nil {
			r.attempts = map[string]int{}
		}
		r.attempts[b.ID]++
		n, limit := r.attempts[b.ID], r.maxAttempts()
		note += fmt.Sprintf(" [attempt %d/%d]", n, limit)
		if n >= limit {
			giveUp = true
			note += "; giving up: labelled " + GaveUpLabel
		}
	}

	// Settle even when interrupted, so a stopped runner does not leave the
	// card claimed by nobody who is working it.
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	var landed bool
	err = r.settleStep(settle, b.ID, "read line master", func() (err error) {
		landed, err = Landed(settle, gitx.Repo{Dir: r.Line.Remote}, b.ID, pre)
		return err
	})
	if err != nil {
		return true, errors.Join(runErr, r.escalateUnsettled(settle, b.ID, err))
	}
	if landed {
		r.logf("%s landed", b.ID)
		r.lastLanded = true
		err := r.settleStep(settle, b.ID, "close", func() error { return r.Beads.Close(settle, b.ID, "landed via line") })
		if err != nil {
			err = r.escalateUnsettled(settle, b.ID, err)
		}
		return true, errors.Join(runErr, err)
	}
	if counts {
		// Interrupted, cord-pulled and engine-failed runs are not the agent giving up.
		note += r.missingReportTail(settle, b.ID, commentsBefore, commentsBeforeErr, tail)
	}
	r.logf("%s: %s", b.ID, note)
	if err := r.settleStep(settle, b.ID, "release", func() error { return r.Beads.Release(settle, b.ID, note) }); err != nil {
		return true, errors.Join(runErr, r.escalateUnsettled(settle, b.ID, err))
	}
	if giveUp {
		delete(r.attempts, b.ID)
		// An andon card holds the line stopped, and Pick offers nothing else
		// meanwhile, so a given-up one must reach the king or the line idles.
		return true, errors.Join(runErr, r.Beads.GiveUp(settle, b.ID, note, slices.Contains(b.Labels, "andon")))
	}
	return true, runErr
}

// failureTailBytes bounds how much of a run's output the runner puts on a
// card when the agent left no failure report of its own.
const failureTailBytes = 2000

// missingReportTail returns text for the release note: the tail of the run's
// output, unless the agent commented on the card during the run (its failure
// report, which the prompt requires). When the comments cannot be counted it
// errs towards including the tail.
func (r *Runner) missingReportTail(ctx context.Context, id string, before int, beforeErr error, tail *tailBuffer) string {
	after, err := r.Beads.CommentCount(ctx, id)
	if err = errors.Join(beforeErr, err); err != nil {
		r.logf("%s: count comments: %v", id, err)
	} else if after > before {
		return ""
	}
	out := tail.String()
	if out == "" {
		return "; the maker left no failure report and no output"
	}
	return "; the maker left no failure report. Tail of its output:\n" + out
}

// tailBuffer keeps the last max bytes written to it. It is safe for the
// concurrent stdout and stderr writers of one container run.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(strings.ToValidUTF8(string(t.buf), ""))
}

// Loop runs Once until ctx ends, sleeping idle between empty polls and after
// errors so a broken setup does not spin. It logs why it stopped: the cause
// of ctx (the signal, when cancelled with one).
func (r *Runner) Loop(ctx context.Context, idle time.Duration) error {
	for {
		worked, err := r.Once(ctx)
		if err != nil {
			r.logf("once: %v", err)
		}
		if ctx.Err() != nil {
			r.logf("exiting on %v", context.Cause(ctx))
			return nil
		}
		// A run that did not land waits out idle before the next try: a failure
		// that happens at once (a clone that cannot start) would otherwise burn
		// every attempt within seconds, before its cause can change.
		if worked && err == nil && r.lastLanded {
			continue
		}
		select {
		case <-ctx.Done():
			r.logf("exiting on %v", context.Cause(ctx))
			return nil
		case <-time.After(idle):
		}
	}
}

// BeadsCLI drives the bd CLI in Dir. Actor names the station in bd's audit
// trail; bd only lets the claim holder release a claim, so it must match the
// agent passed to Claim.
type BeadsCLI struct{ Dir, Actor string }

func (b BeadsCLI) bd(ctx context.Context, args ...string) ([]byte, error) {
	if b.Actor != "" {
		args = append(args, "--actor", b.Actor)
	}
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = b.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bd %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func checkID(id string) error {
	if id == "" || strings.HasPrefix(id, "-") {
		return fmt.Errorf("bad bead id %q", id)
	}
	return nil
}

func (b BeadsCLI) Ready(ctx context.Context, label string) ([]Bead, error) {
	out, err := BeadsCLI{Dir: b.Dir}.bd(ctx, "ready", "--label", label, "--exclude-label", GaveUpLabel, "--limit", "0", "--json")
	if err != nil {
		return nil, err
	}
	beads, err := parseBeads(out)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return slices.DeleteFunc(beads, func(b Bead) bool { return station.IsDeferred(b.Labels, now) }), nil
}

// parseBeads decodes the JSON array `bd ready --json` prints.
func parseBeads(out []byte) ([]Bead, error) {
	var rows []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Priority    int      `json:"priority"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse ready cards: %w", err)
	}
	beads := make([]Bead, 0, len(rows))
	for _, r := range rows {
		beads = append(beads, Bead{ID: r.ID, Title: r.Title, Description: r.Description, Priority: r.Priority, Labels: r.Labels})
	}
	return beads, nil
}

// Claim sets the assignee to agent and the status to in_progress; bd's
// --claim takes the assignee from the actor.
func (b BeadsCLI) Claim(ctx context.Context, id, agent string) error {
	if err := checkID(id); err != nil {
		return err
	}
	_, err := BeadsCLI{Dir: b.Dir, Actor: agent}.bd(ctx, "update", id, "--claim")
	return err
}

func (b BeadsCLI) Close(ctx context.Context, id, reason string) error {
	if err := checkID(id); err != nil {
		return err
	}
	_, err := b.bd(ctx, "close", id, "--reason", reason)
	return err
}

// Claimed lists in_progress cards assigned to agent.
func (b BeadsCLI) Claimed(ctx context.Context, agent string) ([]Bead, error) {
	out, err := b.bd(ctx, "list", "--status", "in_progress", "--assignee", agent, "--limit", "0", "--json")
	if err != nil {
		return nil, err
	}
	return parseBeads(out)
}

// Release reopens the card for another run (clearing the assignee) and
// records the note (bd unclaim --reason comments it) for the next worker.
func (b BeadsCLI) Release(ctx context.Context, id, note string) error {
	if err := checkID(id); err != nil {
		return err
	}
	_, err := b.bd(ctx, "unclaim", id, "--reason", note)
	return err
}

// CommentCount counts the entries `bd comments <id> --json` lists.
func (b BeadsCLI) CommentCount(ctx context.Context, id string) (int, error) {
	if err := checkID(id); err != nil {
		return 0, err
	}
	out, err := b.bd(ctx, "comments", id, "--json")
	if err != nil {
		return 0, err
	}
	var comments []json.RawMessage
	if err := json.Unmarshal(out, &comments); err != nil {
		return 0, fmt.Errorf("parse comments of %s: %w", id, err)
	}
	return len(comments), nil
}

// GiveUp comments reason and adds GaveUpLabel; Ready and Pick then skip the bead.
func (b BeadsCLI) GiveUp(ctx context.Context, id, reason string, escalateToKing bool) error {
	if err := checkID(id); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("give up: reason is required")
	}
	if _, err := b.bd(ctx, "comments", "add", "--", id, "gave up: "+reason); err != nil {
		return err
	}
	labels := []string{GaveUpLabel}
	if escalateToKing {
		labels = append(labels, station.LabelNeedsPrefix+"king")
	}
	for _, l := range labels {
		if _, err := b.bd(ctx, "label", "add", id, l); err != nil {
			return err
		}
	}
	return nil
}

// APICards works cards through the line's /v1/cards routes, so the maker
// never opens the beads database. The server claims and releases as the
// agent of Client's station token.
type APICards struct{ Client station.Client }

func (a APICards) Ready(ctx context.Context, label string) ([]Bead, error) {
	out, err := a.Client.CardsList(ctx, station.ListFilter{Label: label, Ready: true})
	if err != nil {
		return nil, err
	}
	return parseBeads(out)
}

// Claim ignores agent: the server takes it from the station token.
func (a APICards) Claim(ctx context.Context, id, _ string) error {
	return a.Client.CardClaim(ctx, id)
}

func (a APICards) Close(ctx context.Context, id, reason string) error {
	return a.Client.CardClose(ctx, id, reason)
}

func (a APICards) Release(ctx context.Context, id, note string) error {
	return a.Client.CardRelease(ctx, id, note)
}

// CommentCount reads the "comments" the line attaches to a shown card, which
// is an issue object or a one-element array.
func (a APICards) CommentCount(ctx context.Context, id string) (int, error) {
	raw, err := a.Client.CardShow(ctx, id)
	if err != nil {
		return 0, err
	}
	var issues []struct {
		Comments []json.RawMessage `json:"comments"`
	}
	if json.Unmarshal(raw, &issues) != nil {
		var one struct {
			Comments []json.RawMessage `json:"comments"`
		}
		if err := json.Unmarshal(raw, &one); err != nil {
			return 0, fmt.Errorf("parse card %s: %w", id, err)
		}
		return len(one.Comments), nil
	}
	if len(issues) == 0 {
		return 0, fmt.Errorf("card %s not found", id)
	}
	return len(issues[0].Comments), nil
}

// Claimed filters the in_progress list by assignee itself: the route has no
// assignee filter.
func (a APICards) Claimed(ctx context.Context, agent string) ([]Bead, error) {
	out, err := a.Client.CardsList(ctx, station.ListFilter{Status: "in_progress"})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse in_progress cards: %w", err)
	}
	beads, err := parseBeads(out)
	if err != nil {
		return nil, err
	}
	var mine []Bead
	for i, b := range beads {
		if rows[i].Assignee == agent {
			mine = append(mine, b)
		}
	}
	return mine, nil
}

func (a APICards) GiveUp(ctx context.Context, id, reason string, escalateToKing bool) error {
	return a.Client.CardGiveUp(ctx, id, reason, escalateToKing)
}

// DockerEngine runs station containers with a docker-compatible CLI.
type DockerEngine struct {
	Bin            string // default "docker"
	Stdout, Stderr io.Writer
	Log            *log.Logger // reports cleanup failures; optional
}

// stopGrace is how long a cancelled run's CLI gets to stop the container
// before it is killed.
const stopGrace = 30 * time.Second

// remove force-removes the named container. It usually is already gone
// (--rm), so failure is only logged.
func (d DockerEngine) remove(bin, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "rm", "-f", "--", name).CombinedOutput()
	if err != nil && d.Log != nil {
		d.Log.Printf("%s rm -f %s: %v: %s", bin, name, err, strings.TrimSpace(string(out)))
	}
}

// Run execs `<bin> run --rm ...` without a shell. Environment values reach
// the engine through its own environment (`-e K` takes the value from there)
// so the station token never appears in the host's process table.
func (d DockerEngine) Run(ctx context.Context, spec ContainerSpec) (int, error) {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	// The agent runs as a non-root user and needs no capabilities.
	args := []string{"run", "--rm", "--cap-drop=ALL", "--security-opt=no-new-privileges"}
	if strings.HasPrefix(spec.Name, "-") {
		return -1, fmt.Errorf("bad container name %q", spec.Name)
	}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.Pids > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(spec.Pids))
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return -1, fmt.Errorf("bad environment variable name %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := os.Environ()
	for _, k := range keys {
		args = append(args, "-e", k)
		env = append(env, k+"="+spec.Env[k])
	}
	for _, m := range spec.Mounts {
		if strings.Contains(m.Host, ":") || strings.Contains(m.Container, ":") || m.Host == "" || m.Container == "" {
			return -1, fmt.Errorf("bad mount %q -> %q", m.Host, m.Container)
		}
		v := m.Host + ":" + m.Container
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	if spec.Image == "" || strings.HasPrefix(spec.Image, "-") {
		return -1, fmt.Errorf("bad image %q", spec.Image)
	}
	args = append(append(args, spec.Image), spec.Args...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	// On cancellation ask the CLI to stop (it proxies the signal to the
	// container) instead of SIGKILLing it, which would orphan the container.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = stopGrace
	cmd.Stdout, cmd.Stderr = d.Stdout, d.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stderr
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if spec.Output != nil {
		cmd.Stdout = io.MultiWriter(cmd.Stdout, spec.Output)
		cmd.Stderr = io.MultiWriter(cmd.Stderr, spec.Output)
	}
	err := cmd.Run()
	if spec.Name != "" {
		// Whatever ended the CLI, make sure no container outlives the run:
		// the bead is about to be settled and another run may take it.
		d.remove(bin, spec.Name)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
