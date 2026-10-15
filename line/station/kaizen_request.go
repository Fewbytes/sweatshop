package station

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

const (
	heijunkaRole = "heijunka"
	kaizenRole   = "kaizen"
)

// KaizenRequestState is where a request for a Kaizen run stands.
type KaizenRequestState string

const (
	KaizenRequestPending  KaizenRequestState = "pending"  // asked for, not yet answered
	KaizenRequestStarted  KaizenRequestState = "started"  // Kaizen began a run for it
	KaizenRequestDeclined KaizenRequestState = "declined" // Kaizen refused; Reason says why
)

// KaizenRequest is the line's one record of the latest request for a Kaizen
// run, in GET /v1/status. Master is the master the request was made on, so a
// requester can tell a declined request on an unmoved master from a stale one.
type KaizenRequest struct {
	State       KaizenRequestState `json:"state"`
	Master      string             `json:"master"`
	RequestedAt time.Time          `json:"requested_at"`
	Reason      string             `json:"reason,omitempty"`
}

// KaizenAnswer is the body of POST /v1/kaizen/answer. Reason is required for
// a decline.
type KaizenAnswer struct {
	State  KaizenRequestState `json:"state"` // KaizenRequestStarted or KaizenRequestDeclined
	Reason string             `json:"reason,omitempty"`
}

// KaizenAnalysis is the body of POST /v1/kaizen/analysis: the Markdown report
// of one Kaizen run's code-base analysis. Run is the run ID (the run's
// SWEATSHOP_BEAD) and names the stored object.
type KaizenAnalysis struct {
	Run      string `json:"run"`
	Markdown string `json:"markdown"`
}

// kaizenRunID is the shape of a run ID the line accepts into an object key. It
// excludes separators, so a run cannot write outside its own key prefix.
var kaizenRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// KaizenAnalysisKey is the object-store key of a run's analysis report.
func KaizenAnalysisKey(run string) string { return "kaizen/" + run + "/analysis.md" }

// routeKaizen serves the request/answer pair between Heijunka and Kaizen, and
// the analysis reports and signals Kaizen publishes. The line only relays
// requests: a request is refused while the cord is pulled or another is
// pending, and Kaizen alone decides whether to run.
func (s *Server) routeKaizen(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if r.URL.Path == "/v1/kaizen/signals/previous" {
		if st.Role != kaizenRole {
			return "", fail(http.StatusForbidden, "role %q may not read kaizen signals", st.Role)
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			return "", fail(http.StatusMethodNotAllowed, "use GET for %s", r.URL.Path)
		}
		return s.previousKaizenSignals(w, r)
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		return "", fail(http.StatusMethodNotAllowed, "use POST for %s", r.URL.Path)
	}
	switch r.URL.Path {
	case "/v1/kaizen/request":
		if st.Role != heijunkaRole {
			return "", fail(http.StatusForbidden, "role %q may not request a kaizen run", st.Role)
		}
		return s.requestKaizen(w, r)
	case "/v1/kaizen/answer":
		if st.Role != kaizenRole {
			return "", fail(http.StatusForbidden, "role %q may not answer a kaizen request", st.Role)
		}
		return s.answerKaizen(w, r)
	case "/v1/kaizen/analysis":
		if st.Role != kaizenRole {
			return "", fail(http.StatusForbidden, "role %q may not publish a kaizen analysis", st.Role)
		}
		return s.publishKaizenAnalysis(w, r)
	case "/v1/kaizen/signals":
		if st.Role != kaizenRole {
			return "", fail(http.StatusForbidden, "role %q may not publish kaizen signals", st.Role)
		}
		return s.publishKaizenSignals(w, r)
	default:
		return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
	}
}

func (s *Server) publishKaizenAnalysis(w http.ResponseWriter, r *http.Request) (string, error) {
	if s.Reports == nil {
		return "", fail(http.StatusServiceUnavailable, "no object store configured")
	}
	var a KaizenAnalysis
	if err := decodeBody(w, r, &a); err != nil {
		return "", err
	}
	if !kaizenRunID.MatchString(a.Run) {
		return "", fail(http.StatusBadRequest, "run %q is not a valid run ID", a.Run)
	}
	if a.Markdown == "" {
		return "", fail(http.StatusBadRequest, "markdown is empty")
	}
	key := KaizenAnalysisKey(a.Run)
	if err := s.Reports.Put(r.Context(), key, "text/markdown; charset=utf-8", []byte(a.Markdown)); err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key})
	return "kaizen analysis " + key, nil
}

func (s *Server) requestKaizen(w http.ResponseWriter, r *http.Request) (string, error) {
	master, err := s.Guard.Repo.Ref(r.Context(), s.Guard.Ref)
	if err != nil {
		return "", err
	}
	id, _, err := s.Guard.Stopped(r.Context())
	if err != nil {
		return "", err
	}
	if id != "" {
		return "", fail(http.StatusConflict, "the andon cord is pulled (%s)", id)
	}
	s.kaizenMu.Lock()
	defer s.kaizenMu.Unlock()
	if s.kaizenRequest != nil && s.kaizenRequest.State == KaizenRequestPending {
		return "", fail(http.StatusConflict, "a kaizen request is already pending")
	}
	req := &KaizenRequest{State: KaizenRequestPending, Master: master, RequestedAt: time.Now()}
	s.kaizenRequest = req
	writeJSON(w, http.StatusOK, req)
	return "kaizen request on " + master, nil
}

func (s *Server) answerKaizen(w http.ResponseWriter, r *http.Request) (string, error) {
	var a KaizenAnswer
	if err := decodeBody(w, r, &a); err != nil {
		return "", err
	}
	if a.State != KaizenRequestStarted && a.State != KaizenRequestDeclined {
		return "", fail(http.StatusBadRequest, "state %q is not %s or %s", a.State, KaizenRequestStarted, KaizenRequestDeclined)
	}
	if (a.State == KaizenRequestDeclined) != (a.Reason != "") {
		return "", fail(http.StatusBadRequest, "a reason goes with state %s and only with it", KaizenRequestDeclined)
	}
	s.kaizenMu.Lock()
	defer s.kaizenMu.Unlock()
	if s.kaizenRequest == nil || s.kaizenRequest.State != KaizenRequestPending {
		return "", fail(http.StatusConflict, "no kaizen request is pending")
	}
	s.kaizenRequest.State, s.kaizenRequest.Reason = a.State, a.Reason
	writeJSON(w, http.StatusOK, s.kaizenRequest)
	return "kaizen request " + string(a.State), nil
}

func (s *Server) latestKaizenRequest() *KaizenRequest {
	s.kaizenMu.Lock()
	defer s.kaizenMu.Unlock()
	if s.kaizenRequest == nil {
		return nil
	}
	req := *s.kaizenRequest
	return &req
}

// RequestKaizen asks the line for a Kaizen run; the caller's token must be
// the heijunka station's.
func (c Client) RequestKaizen(ctx context.Context) error {
	_, err := c.postJSON(ctx, "/v1/kaizen/request", struct{}{})
	return err
}

// AnswerKaizen tells the line whether Kaizen started a run for the pending
// request; the caller's token must be a kaizen station's.
func (c Client) AnswerKaizen(ctx context.Context, a KaizenAnswer) error {
	_, err := c.postJSON(ctx, "/v1/kaizen/answer", a)
	return err
}

// PublishKaizenAnalysis stores the Markdown analysis report of a Kaizen run in
// the line's object store; the caller's token must be a kaizen station's. It
// returns the object key.
func (c Client) PublishKaizenAnalysis(ctx context.Context, a KaizenAnalysis) (string, error) {
	return c.postJSONForKey(ctx, "/v1/kaizen/analysis", a)
}
