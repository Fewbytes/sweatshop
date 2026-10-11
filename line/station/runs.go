package station

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

const (
	defaultRunLimit = 100
	maxRunLimit     = 1000
	slowestRuns     = 5
)

// RunInfo is one stored run. ReportKey and HTMLKey are object-store keys of
// the JUnit XML and HTML reports ("" when that upload failed).
type RunInfo struct {
	Commit     string    `json:"commit"`
	Tree       string    `json:"tree"`
	Role       string    `json:"role"`
	AgentID    string    `json:"agent_id"`
	Runner     string    `json:"runner"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Checks     int       `json:"checks"`
	ChecksPass int       `json:"checks_passed"`
	ChecksFail int       `json:"checks_failed"`
	Passed     bool      `json:"passed"`
	ReportKey  string    `json:"report_key,omitempty"`
	HTMLKey    string    `json:"html_key,omitempty"`
}

// RunsSummary aggregates every run matching the filter, not just the page.
type RunsSummary struct {
	Runs         int       `json:"runs"`
	Passed       int       `json:"passed"`
	Failed       int       `json:"failed"`
	ChecksFailed int       `json:"checks_failed"`
	TotalMS      int64     `json:"total_duration_ms"`
	AvgMS        int64     `json:"avg_duration_ms"`
	MaxMS        int64     `json:"max_duration_ms"`
	Slowest      []RunInfo `json:"slowest"`
	FlakyTrees   []string  `json:"flaky_trees"` // trees that both passed and failed
}

// RunsResponse is the body of GET /v1/runs. Runs are newest first. The line
// stores per-run check counts, not per-test results, so there is no skip count.
type RunsResponse struct {
	Summary RunsSummary `json:"summary"`
	Runs    []RunInfo   `json:"runs"`
}

// runs serves GET /v1/runs?limit=&role=&commit=&passed=: stored run stats,
// read-only, for Kaizen and andon workers.
func (s *Server) runs(w http.ResponseWriter, r *http.Request) (string, error) {
	if s.RunsPath == "" {
		return "", fail(http.StatusServiceUnavailable, "no run store")
	}
	q := r.URL.Query()
	limit := defaultRunLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxRunLimit {
			return "", fail(http.StatusBadRequest, "limit must be 1..%d", maxRunLimit)
		}
		limit = n
	}
	var wantPassed *bool
	if v := q.Get("passed"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", fail(http.StatusBadRequest, "passed must be true or false")
		}
		wantPassed = &b
	}
	stored, err := report.ReadJSONL(s.RunsPath)
	if err != nil {
		return "", err
	}
	var all []RunInfo
	for i := len(stored) - 1; i >= 0; i-- { // newest first
		st := stored[i]
		if (q.Get("role") != "" && st.Role != q.Get("role")) ||
			(q.Get("commit") != "" && st.Commit != q.Get("commit")) ||
			(wantPassed != nil && st.Passed != *wantPassed) {
			continue
		}
		all = append(all, RunInfo{
			Commit: st.Commit, Tree: st.Tree, Role: st.Role, AgentID: st.AgentID, Runner: st.Runner,
			StartedAt: st.StartedAt, DurationMS: st.DurationMS,
			Checks: st.Checks, ChecksPass: st.Checks - st.Failures, ChecksFail: st.Failures,
			Passed: st.Passed, ReportKey: st.ReportKey, HTMLKey: st.HTMLKey,
		})
	}
	sum := RunsSummary{Runs: len(all), Slowest: []RunInfo{}, FlakyTrees: []string{}}
	outcomes := map[string][2]bool{} // tree -> seen [fail, pass]
	for _, ri := range all {
		if ri.Passed {
			sum.Passed++
		} else {
			sum.Failed++
		}
		sum.ChecksFailed += ri.ChecksFail
		sum.TotalMS += ri.DurationMS
		sum.MaxMS = max(sum.MaxMS, ri.DurationMS)
		if ri.Tree != "" {
			o := outcomes[ri.Tree]
			if ri.Passed {
				o[1] = true
			} else {
				o[0] = true
			}
			outcomes[ri.Tree] = o
		}
	}
	if len(all) > 0 {
		sum.AvgMS = sum.TotalMS / int64(len(all))
	}
	for tree, o := range outcomes {
		if o[0] && o[1] {
			sum.FlakyTrees = append(sum.FlakyTrees, tree)
		}
	}
	sort.Strings(sum.FlakyTrees)
	byDuration := append([]RunInfo(nil), all...)
	sort.SliceStable(byDuration, func(i, j int) bool { return byDuration[i].DurationMS > byDuration[j].DurationMS })
	sum.Slowest = append(sum.Slowest, byDuration[:min(slowestRuns, len(byDuration))]...)
	page := all[:min(limit, len(all))]
	if page == nil {
		page = []RunInfo{}
	}
	writeJSON(w, http.StatusOK, RunsResponse{Summary: sum, Runs: page})
	return "runs " + strconv.Itoa(len(page)), nil
}
