package kaizen

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/maker"
	"github.com/Fewbytes/sweatshop/line/station"
)

// DefaultInterval is the least time between two runs. Kaizen is a standing
// counterweight, not a flow stage: one small change a day keeps the churn it
// causes below what Makers can absorb.
const DefaultInterval = 24 * time.Hour

// DefaultPoll is how often the loop looks at master between runs.
const DefaultPoll = time.Minute

var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// RequestAPI is the line's Kaizen request channel: Heijunka files a request
// and Kaizen answers it.
type RequestAPI interface {
	Status(ctx context.Context) (station.Status, error)
	AnswerKaizen(ctx context.Context, a station.KaizenAnswer) error
}

// Runner is the Kaizen station's host-side loop. A run starts when
// Interval has passed since the last one and master differs from the master
// that run started on: an unmoved master has no new hotspot, so Kaizen
// sleeps instead of making work. A request from Heijunka stands in for the
// Interval only; the master and andon guards still apply, and a request they
// refuse is declined, which the line records. The state lives in memory; a
// restarted runner runs once at once, which is cheap and safe.
type Runner struct {
	Memory, CPUs string
	Pids         int

	Line     linedir.Line
	Station  linedir.Station
	Engine   maker.Engine
	URL      string // as seen from inside the container; default http://host.containers.internal:<port>
	Token    string // empty reads Station.TokenFile
	Creds    map[string]string
	Timeout  time.Duration // bounds one container run; default maker.DefaultTimeout
	Interval time.Duration // default DefaultInterval
	// Heartbeats tells the line this station is alive, idle or running, and
	// when it last ran and may next; nil sends none. Commit is the binary's
	// commit, reported with each heartbeat.
	Heartbeats maker.Heartbeater
	// Requests carries Heijunka's run requests; nil ignores them.
	Requests RequestAPI
	Commit   string
	Log      *log.Logger
	Now      func() time.Time // default time.Now

	lastRun    time.Time
	lastMaster string
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Printf(format, args...)
	}
}

// beat sends one heartbeat; a line that cannot be reached must not stop
// Kaizen, so a failure is only logged. A run that has never happened leaves
// LastRun nil and is eligible now.
func (r *Runner) beat(ctx context.Context, state station.StationState, card string, now time.Time, interval time.Duration) {
	if r.Heartbeats == nil {
		return
	}
	hb := station.Heartbeat{State: state, Card: card, Commit: r.Commit}
	next := now
	if !r.lastRun.IsZero() {
		last := r.lastRun
		hb.LastRun, next = &last, last.Add(interval)
	}
	hb.NextRun = &next
	if err := r.Heartbeats.Heartbeat(ctx, hb); err != nil && ctx.Err() == nil {
		r.logf("heartbeat: %v", err)
	}
}

// requestPending reports whether Heijunka has a request waiting for an answer.
func (r *Runner) requestPending(ctx context.Context) (bool, error) {
	if r.Requests == nil {
		return false, nil
	}
	st, err := r.Requests.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("read kaizen request: %w", err)
	}
	return st.KaizenRequest != nil && st.KaizenRequest.State == station.KaizenRequestPending, nil
}

// declineRequest answers a pending request with the reason Kaizen will not
// run; without one it does nothing. The line records the answer, so the
// request is not asked again on this master.
func (r *Runner) declineRequest(ctx context.Context, requested bool, reason string) error {
	if !requested {
		return nil
	}
	r.logf("declining kaizen request: %s", reason)
	if err := r.Requests.AnswerKaizen(ctx, station.KaizenAnswer{State: station.KaizenRequestDeclined, Reason: reason}); err != nil {
		return fmt.Errorf("answer kaizen request: %w", err)
	}
	return nil
}

// Once starts one run if the schedule and master allow it and reports
// whether it did. A run that lands nothing is not an error: the agent may
// find nothing worth improving.
func (r *Runner) Once(ctx context.Context) (bool, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	r.beat(ctx, station.StateIdle, "", now(), interval)
	remote := gitx.Repo{Dir: r.Line.Remote}
	master, err := remote.Ref(ctx, "refs/heads/master")
	if err != nil {
		return false, fmt.Errorf("read line master: %w", err)
	}
	requested, err := r.requestPending(ctx)
	if err != nil {
		return false, err
	}
	if !r.lastRun.IsZero() && (master == r.lastMaster || (!requested && now().Sub(r.lastRun) < interval)) {
		return false, r.declineRequest(ctx, requested, "master has not moved since the last run")
	}
	// Merges are refused while the cord is pulled, and only a fix may land.
	stopped, err := remote.RefExists(ctx, guard.StopRef)
	if err != nil {
		return false, fmt.Errorf("read andon state: %w", err)
	}
	if stopped {
		return false, r.declineRequest(ctx, requested, "the andon cord is pulled")
	}
	token := r.Token
	if token == "" {
		if token, err = r.Station.Token(); err != nil {
			return false, fmt.Errorf("station %s token: %w", r.Station.Name, err)
		}
	}

	if requested {
		if err := r.Requests.AnswerKaizen(ctx, station.KaizenAnswer{State: station.KaizenRequestStarted}); err != nil {
			return false, fmt.Errorf("answer kaizen request: %w", err)
		}
	}
	start := now()
	runID := "kaizen-" + start.UTC().Format("20060102T150405Z")
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
	env["SWEATSHOP_BEAD"] = runID
	env["SWEATSHOP_KAIZEN"] = "1"
	env["SWEATSHOP_AGENT_ID"] = r.Station.AgentID
	env["SWEATSHOP_PROMPT"] = Prompt(runID)
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = maker.DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r.logf("%s: starting on master %s", runID, master)
	stopBeats := maker.BeatEveryPoll(ctx, func(ctx context.Context) {
		r.beat(ctx, station.StateWorking, runID, now(), interval)
	})
	code, runErr := r.Engine.Run(runCtx, maker.ContainerSpec{
		Image:   r.Line.Image,
		Name:    unsafeNameChars.ReplaceAllString("sweatshop-"+r.Station.Name+"-"+runID, "_"),
		Env:     env,
		Mounts:  []maker.Mount{{Host: r.Line.Remote, Container: "/line/remote.git", ReadOnly: true}},
		Network: r.Line.Network,
		Memory:  r.Memory, CPUs: r.CPUs, Pids: r.Pids,
	})
	stopBeats()
	if ctx.Err() != nil {
		return true, errors.Join(runErr, context.Cause(ctx)) // interrupted: do not count as a run
	}
	r.lastRun, r.lastMaster = start, master

	landed, err := maker.Landed(context.WithoutCancel(ctx), remote, runID, master)
	if err != nil {
		return true, errors.Join(runErr, fmt.Errorf("%s: read line master: %w", runID, err))
	}
	r.logf("%s: container exited %d (err %v), landed %t", runID, code, runErr, landed)
	return true, runErr
}

// Loop runs Once every poll until ctx ends, and logs why it stopped.
func (r *Runner) Loop(ctx context.Context, poll time.Duration) error {
	for {
		if _, err := r.Once(ctx); err != nil {
			r.logf("once: %v", err)
		}
		select {
		case <-ctx.Done():
			r.logf("exiting on %v", context.Cause(ctx))
			return nil
		case <-time.After(poll):
		}
	}
}
