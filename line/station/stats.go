package station

import (
	"net/http"
	"time"

	"github.com/Fewbytes/sweatshop/line/cistats"
)

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
		if since, err = cistats.ParseSince(r.URL.Query().Get("since"), now); err != nil {
			return "", fail(http.StatusBadRequest, "%v", err)
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
