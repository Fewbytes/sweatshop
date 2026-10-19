package station

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

// recordEvent appends to the line's event log. A failure is logged, not
// returned: the event describes something that already happened, and the log
// is for diagnosis, so it must not undo or fail the action. Without a line
// database nothing is recorded.
func (s *Server) recordEvent(ctx context.Context, st *Station, kind linestore.EventKind, card, detail string) {
	if s.Store == nil {
		return
	}
	e := linestore.Event{At: time.Now(), Kind: kind, Card: card, Station: st.Name, Detail: detail}
	if err := s.Store.RecordEvent(ctx, e); err != nil {
		log.Printf("station api: record %s event: %v", kind, err)
	}
}

// routeEvents serves the line's event log. Any station may read it (GET
// ?since=36h&card=ID&station=NAME, oldest first); only the heijunka station
// appends (POST), and only reaper actions, because it acts on cards through bd
// rather than through this API. Every other event is recorded by the route
// that performs it.
func (s *Server) routeEvents(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no line database")
	}
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		since, err := sinceQuery(q, time.Now())
		if err != nil {
			return "", err
		}
		f := linestore.EventFilter{Since: since, Card: q.Get("card"), Station: q.Get("station")}
		if f.Card != "" {
			if err := validID(f.Card); err != nil {
				return "", err
			}
		}
		if f.Station != "" && !wordRe.MatchString(f.Station) {
			return "", fail(http.StatusBadRequest, "invalid station %q", f.Station)
		}
		events, err := s.Store.Events(r.Context(), f)
		if err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, events)
		return "events", nil
	case http.MethodPost:
		if st.Role != heijunkaRole {
			return "", fail(http.StatusForbidden, "role %q may not record line events", st.Role)
		}
		var body struct {
			Card   string `json:"card"`
			Detail string `json:"detail"`
		}
		if err := decodeBody(w, r, &body); err != nil {
			return "", err
		}
		if err := validID(body.Card); err != nil {
			return "", err
		}
		if err := s.Store.RecordEvent(r.Context(), linestore.Event{At: time.Now(), Kind: linestore.EventReap, Card: body.Card, Station: st.Name, Detail: body.Detail}); err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return "reap event " + body.Card, nil
	}
	w.Header().Set("Allow", "GET, POST")
	return "", fail(http.StatusMethodNotAllowed, "use GET or POST for %s", r.URL.Path)
}

// Events returns the line's events matching f, oldest first. Since is a
// --since value ("" for all history).
func (c Client) Events(ctx context.Context, since, card, station string) ([]linestore.Event, error) {
	q := url.Values{}
	for k, v := range map[string]string{"since": since, "card": card, "station": station} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var events []linestore.Event
	_, err := c.do(ctx, http.MethodGet, "/v1/events", q, nil, &events)
	return events, err
}

// RecordReap logs that the reaper acted on card, with detail saying why; the
// caller's token must be the heijunka station's.
func (c Client) RecordReap(ctx context.Context, card, detail string) error {
	_, err := c.postJSON(ctx, "/v1/events", map[string]string{"card": card, "detail": detail})
	return err
}
