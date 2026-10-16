package station

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// MakerPollInterval is how often a maker loop polls for work, and so how
// often it sends a heartbeat. MakerSilenceLimit is how long a station may stay
// silent before the line counts it dead: three missed polls.
const (
	MakerPollInterval = 60 * time.Second
	MakerSilenceLimit = 3 * MakerPollInterval
)

// MaxClockSkew is how far a station's clock may differ from the line's before
// MakerWatch warns. A podman machine VM clock drifts after the host sleeps,
// and station containers inherit it: commit dates and logs come out wrong, and
// a large skew breaks TLS to the model API.
const MaxClockSkew = 2 * time.Minute

// StationState is what a station reports it is doing; StateUnseen is what the
// line reports for a maker that has never sent a heartbeat.
type StationState string

const (
	StateIdle    StationState = "idle"
	StateWorking StationState = "working"
	StateUnseen  StationState = "unseen"
)

// Heartbeat is the body of POST /v1/stations/heartbeat. The station is the
// one the bearer token selects.
type Heartbeat struct {
	State  StationState `json:"state"`            // StateIdle or StateWorking
	Card   string       `json:"card,omitempty"`   // the card being worked; required when working
	Commit string       `json:"commit,omitempty"` // commit of the station's binary
	// IdleReason says why an idle station took no card; only with StateIdle.
	IdleReason string `json:"idle_reason,omitempty"`
	// Clock is the station's own time when it sent the heartbeat; Client.Heartbeat
	// fills it in. Nil from a station that predates the field.
	Clock *time.Time `json:"clock,omitempty"`
	// LastRun and NextRun are a scheduled station's last run start and the
	// earliest time of its next; nil for stations that run no schedule or have
	// not run yet.
	LastRun *time.Time `json:"last_run,omitempty"`
	NextRun *time.Time `json:"next_run,omitempty"`
}

// StationStatus is one station's liveness in GET /v1/status.
type StationStatus struct {
	Name   string       `json:"name"`
	Role   string       `json:"role"`
	State  StationState `json:"state"`
	Card   string       `json:"card,omitempty"`
	Commit string       `json:"commit,omitempty"`
	// IdleReason is as in Heartbeat. LastCard is the card the station worked
	// before its current state, so a stall shows what it last did.
	IdleReason string     `json:"idle_reason,omitempty"`
	LastCard   string     `json:"last_card,omitempty"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	// ClockSkewSeconds is the station's clock minus the line's at the last
	// heartbeat: negative when the station runs behind. Zero when unreported.
	ClockSkewSeconds int64 `json:"clock_skew_seconds,omitempty"`
	// Alive is true when the station heartbeated within MakerSilenceLimit.
	Alive bool `json:"alive"`
	// LastRun and NextRun are as in Heartbeat, from the last heartbeat.
	LastRun *time.Time `json:"last_run,omitempty"`
	NextRun *time.Time `json:"next_run,omitempty"`
}

type seenStation struct {
	Heartbeat
	at       time.Time
	lastCard string // the card the station worked before this heartbeat's
}

// liveness keeps the last heartbeat per station name, in memory: after a
// restart of line serve every station is unseen until its next poll.
type liveness struct {
	mu   sync.Mutex
	seen map[string]seenStation
}

func (l *liveness) beat(name string, hb Heartbeat, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]seenStation{}
	}
	prev := l.seen[name]
	lastCard := prev.lastCard
	if prev.State == StateWorking && prev.Card != hb.Card {
		lastCard = prev.Card
	}
	l.seen[name] = seenStation{Heartbeat: hb, at: now, lastCard: lastCard}
}

// snapshot lists every maker station and every other station that has
// heartbeated, in configuration order.
func (l *liveness) snapshot(stations []*Station, now time.Time) []StationStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []StationStatus{}
	for _, st := range stations {
		seen, ok := l.seen[st.Name]
		if !ok {
			if st.Role == makerRole {
				out = append(out, StationStatus{Name: st.Name, Role: st.Role, State: StateUnseen})
			}
			continue
		}
		at := seen.at
		var skew time.Duration
		if seen.Clock != nil {
			skew = seen.Clock.Sub(at)
		}
		out = append(out, StationStatus{
			Name: st.Name, Role: st.Role, State: seen.State, Card: seen.Card, Commit: seen.Commit,
			IdleReason: seen.IdleReason, LastCard: seen.lastCard,
			LastSeen: &at, ClockSkewSeconds: int64(skew / time.Second), Alive: now.Sub(at) <= MakerSilenceLimit,
			LastRun: seen.LastRun, NextRun: seen.NextRun,
		})
	}
	return out
}

// MakersAlive counts the maker stations that heartbeated recently.
func (s Status) MakersAlive() int {
	n := 0
	for _, st := range s.Stations {
		if st.Role == makerRole && st.Alive {
			n++
		}
	}
	return n
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		return "", fail(http.StatusMethodNotAllowed, "use POST for %s", r.URL.Path)
	}
	var hb Heartbeat
	if err := decodeBody(w, r, &hb); err != nil {
		return "", err
	}
	if hb.State != StateIdle && hb.State != StateWorking {
		return "", fail(http.StatusBadRequest, "state %q is not %s or %s", hb.State, StateIdle, StateWorking)
	}
	if (hb.State == StateWorking) != (hb.Card != "") {
		return "", fail(http.StatusBadRequest, "a card goes with state %s and only with it", StateWorking)
	}
	if hb.IdleReason != "" && hb.State != StateIdle {
		return "", fail(http.StatusBadRequest, "an idle reason goes only with state %s", StateIdle)
	}
	s.liveness.beat(st.Name, hb, time.Now())
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return fmt.Sprintf("heartbeat %s %s", hb.State, hb.Card), nil
}

// MakerWatch posts to the channel once when no maker has been seen for
// MakerSilenceLimit, and once more when one returns: a line with no maker is
// a stopped line (jidoka), and no one else would notice.
type MakerWatch struct {
	Server *Server
	// Announce posts to the king's channel.
	Announce func(ctx context.Context, text string) error
	// Started is when line serve began; with no heartbeat yet, silence is
	// counted from it.
	Started time.Time

	alerted bool
	// skewed holds the stations already reported as out of step.
	skewed map[string]bool
}

// Check posts the alert or the all-clear if the state changed. A failed post
// leaves the state as it was, so the next Check retries.
func (w *MakerWatch) Check(ctx context.Context, now time.Time) error {
	skewErr := w.checkClockSkew(ctx, now)
	last, alive := w.lastMakerSeen(now)
	if alive != w.alerted { // healthy and quiet, or already alerted
		return skewErr
	}
	text := "a maker is alive again"
	if alive {
		w.alerted = false
	} else {
		text = fmt.Sprintf("no maker alive since %s", last.UTC().Format(time.RFC3339))
		w.alerted = true
	}
	if err := w.Announce(ctx, text); err != nil {
		w.alerted = !w.alerted
		return errors.Join(skewErr, fmt.Errorf("announce %q: %w", text, err))
	}
	return skewErr
}

// checkClockSkew logs and posts once per station whose clock differs from the
// line's by more than MaxClockSkew, and once more when it is back in step. Only
// live stations count: a silent station's last skew is stale. A failed post
// leaves the state as it was, so the next Check retries.
func (w *MakerWatch) checkClockSkew(ctx context.Context, now time.Time) error {
	if w.skewed == nil {
		w.skewed = map[string]bool{}
	}
	var errs []error
	for _, st := range w.Server.liveness.snapshot(w.Server.Stations, now) {
		skew := time.Duration(st.ClockSkewSeconds) * time.Second
		skewed := st.Alive && (skew > MaxClockSkew || skew < -MaxClockSkew)
		if skewed == w.skewed[st.Name] {
			continue
		}
		text := fmt.Sprintf("station %s clock is back in step with the line", st.Name)
		if skewed {
			text = fmt.Sprintf("station %s clock is %s off the line's (negative is behind); on a podman machine run: podman machine ssh \"sudo date -u -s @$(date +%%s)\"", st.Name, skew)
		}
		log.Printf("clock skew: %s", text)
		if err := w.Announce(ctx, text); err != nil {
			errs = append(errs, fmt.Errorf("announce %q: %w", text, err))
			continue
		}
		w.skewed[st.Name] = skewed
	}
	return errors.Join(errs...)
}

// lastMakerSeen returns when the newest maker heartbeat arrived (Started when
// there is none) and whether that is recent enough.
func (w *MakerWatch) lastMakerSeen(now time.Time) (time.Time, bool) {
	last := w.Started
	for _, st := range w.Server.liveness.snapshot(w.Server.Stations, now) {
		if st.Role == makerRole && st.LastSeen != nil && st.LastSeen.After(last) {
			last = *st.LastSeen
		}
	}
	return last, now.Sub(last) <= MakerSilenceLimit
}

// Run checks every MakerPollInterval until ctx ends.
func (w *MakerWatch) Run(ctx context.Context) {
	t := time.NewTicker(MakerPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := w.Check(ctx, now); err != nil && ctx.Err() == nil {
				log.Printf("maker watch: %v", err)
			}
		}
	}
}
