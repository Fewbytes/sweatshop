package station

import (
	"net/http"
	"net/url"
	"time"

	"github.com/Fewbytes/sweatshop/line/cistats"
)

// sinceQuery reads the ?since= window (36h, 7d; empty means all history)
// relative to now. A malformed window is a 400.
func sinceQuery(q url.Values, now time.Time) (time.Time, error) {
	since, err := cistats.ParseSince(q.Get("since"), now)
	if err != nil {
		return time.Time{}, fail(http.StatusBadRequest, "%v", err)
	}
	return since, nil
}

// ciStats serves GET /v1/stats?since= (a cistats.Summary as JSON) and GET
// /metrics (the same numbers over cistats.MetricsWindow, as Prometheus text for
// the host's VictoriaMetrics to scrape with a station bearer token).
func (s *Server) ciStats(w http.ResponseWriter, r *http.Request, metrics bool) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no run store")
	}
	now := time.Now()
	since := now.Add(-cistats.MetricsWindow)
	if !metrics {
		var err error
		if since, err = sinceQuery(r.URL.Query(), now); err != nil {
			return "", err
		}
	}
	runs, err := s.Store.Runs(r.Context())
	if err != nil {
		return "", err
	}
	tests, err := s.Store.Tests(r.Context())
	if err != nil {
		return "", err
	}
	sum := cistats.Compute(runs, tests, since)
	if !metrics {
		writeJSON(w, http.StatusOK, sum)
		return "stats", nil
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, err = w.Write([]byte(sum.Prometheus(cistats.Compute(runs, nil, time.Time{}))))
	return "metrics", err
}
