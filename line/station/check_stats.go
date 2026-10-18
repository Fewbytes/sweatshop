package station

import (
	"net/http"
	"time"

	"github.com/Fewbytes/sweatshop/line/cistats"
)

// checkStats serves GET /v1/ci/stats?since=&check=&commit= (a
// []cistats.CheckSummary as JSON): per-check run counts, failure rate,
// duration percentiles and flaky commits, read-only for every station.
func (s *Server) checkStats(w http.ResponseWriter, r *http.Request) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no run store")
	}
	q := r.URL.Query()
	since, err := cistats.ParseSince(q.Get("since"), time.Now())
	if err != nil {
		return "", fail(http.StatusBadRequest, "%v", err)
	}
	rows, err := s.Store.Checks(r.Context())
	if err != nil {
		return "", err
	}
	sums := cistats.ComputeChecks(rows, cistats.CheckFilter{Since: since, Check: q.Get("check"), Commit: q.Get("commit")})
	writeJSON(w, http.StatusOK, sums)
	return "ci stats", nil
}
