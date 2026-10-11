package station

import (
	"context"
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
}

// StationStatus is one station's liveness in GET /v1/status.
type StationStatus struct {
	Name     string       `json:"name"`
	Role     string       `json:"role"`
	State    StationState `json:"state"`
	Card     string       `json:"card,omitempty"`
	Commit   string       `json:"commit,omitempty"`
	LastSeen *time.Time   `json:"last_seen,omitempty"`
	// Alive is true when the station heartbeated within MakerSilenceLimit.
	Alive bool `json:"alive"`
}

type seenStation struct {
	Heartbeat
	at time.Time
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
	l.seen[name] = seenStation{Heartbeat: hb, at: now}
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
		out = append(out, StationStatus{
			Name: st.Name, Role: st.Role, State: seen.State, Card: seen.Card, Commit: seen.Commit,
			LastSeen: &at, Alive: now.Sub(at) <= MakerSilenceLimit,
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
}

// Check posts the alert or the all-clear if the state changed. A failed post
// leaves the state as it was, so the next Check retries.
func (w *MakerWatch) Check(ctx context.Context, now time.Time) error {
	last, alive := w.lastMakerSeen(now)
	if alive != w.alerted { // healthy and quiet, or already alerted
		return nil
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
		return fmt.Errorf("announce %q: %w", text, err)
	}
	return nil
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
