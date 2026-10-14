package station

import (
	"context"
	"net/http"
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

// routeKaizen serves the request/answer pair between Heijunka and Kaizen.
// The line only relays: a request is refused while the cord is pulled or
// another is pending, and Kaizen alone decides whether to run.
func (s *Server) routeKaizen(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
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
	default:
		return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
	}
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
