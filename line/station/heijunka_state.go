package station

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

// HeijunkaState is the read-only view of heijunka's decisions and the offer
// queue, for the viceroy to supervise flow.
type HeijunkaState struct {
	// Levels are the targets heijunka last published; WIPLimit 0 means none.
	Levels linestore.Levels `json:"levels"`
	// InProgress is the number of line cards makers hold now.
	InProgress int `json:"in_progress"`
	// Offered are the ready cards labelled LabelLine, which makers may pull.
	Offered []string `json:"offered"`
	// Unlabelled are ready cards without LabelLine, which makers are never offered.
	Unlabelled []string `json:"unlabelled"`
	// Idle says why each live idle maker holds no card.
	Idle []IdleMaker `json:"idle,omitempty"`
}

// IdleMaker is a live maker station holding no card, with the reason.
type IdleMaker struct {
	Station string `json:"station"`
	Reason  string `json:"reason"`
}

// cardRows decodes the id and labels of a card list.
func cardRows(out json.RawMessage) ([]struct {
	ID     string   `json:"id"`
	Labels []string `json:"labels"`
}, error) {
	var rows []struct {
		ID     string   `json:"id"`
		Labels []string `json:"labels"`
	}
	err := json.Unmarshal(out, &rows)
	return rows, err
}

// heijunkaState gathers levels, the offer queue and per-maker idle reasons.
func (s *Server) heijunkaState(ctx context.Context) (HeijunkaState, error) {
	var hs HeijunkaState
	levels, err := s.Store.Levels(ctx)
	if err != nil {
		return hs, err
	}
	hs.Levels = levels
	readyOut, err := s.Cards.List(ctx, ListFilter{Ready: true})
	if err != nil {
		return hs, fmt.Errorf("list ready cards: %w", err)
	}
	ready, err := cardRows(readyOut)
	if err != nil {
		return hs, fmt.Errorf("parse ready cards: %w", err)
	}
	hs.Offered, hs.Unlabelled = []string{}, []string{}
	for _, r := range ready {
		if slices.Contains(r.Labels, LabelLine) {
			hs.Offered = append(hs.Offered, r.ID)
		} else {
			hs.Unlabelled = append(hs.Unlabelled, r.ID)
		}
	}
	heldOut, err := s.Cards.List(ctx, ListFilter{Status: "in_progress", Label: LabelLine})
	if err != nil {
		return hs, fmt.Errorf("list in-progress cards: %w", err)
	}
	held, err := cardRows(heldOut)
	if err != nil {
		return hs, fmt.Errorf("parse in-progress cards: %w", err)
	}
	hs.InProgress = len(held)
	for _, st := range s.liveness.snapshot(s.Stations, time.Now()) {
		if st.Role != "maker" || !st.Alive || st.State != StateIdle {
			continue
		}
		hs.Idle = append(hs.Idle, IdleMaker{Station: st.Name, Reason: hs.idleReason()})
	}
	return hs, nil
}

func (hs HeijunkaState) idleReason() string {
	switch {
	case hs.Levels.WIPLimit > 0 && hs.InProgress >= hs.Levels.WIPLimit:
		return fmt.Sprintf("WIP cap reached: %d in progress, limit %d", hs.InProgress, hs.Levels.WIPLimit)
	case len(hs.Offered) > 0:
		return fmt.Sprintf("%d offered cards wait; the maker has not pulled one", len(hs.Offered))
	case len(hs.Unlabelled) > 0:
		return fmt.Sprintf("label mismatch: %d ready cards lack label %s", len(hs.Unlabelled), LabelLine)
	}
	return "no offerable cards"
}

// routeHeijunkaState serves GET /v1/heijunka/state to any station.
func (s *Server) routeHeijunkaState(w http.ResponseWriter, r *http.Request) (string, error) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		return "", fail(http.StatusMethodNotAllowed, "use GET for %s", r.URL.Path)
	}
	if s.Store == nil || s.Cards == nil {
		return "", fail(http.StatusServiceUnavailable, "no line database or card tracker")
	}
	hs, err := s.heijunkaState(r.Context())
	if err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, hs)
	return "heijunka state", nil
}

// HeijunkaState returns heijunka's levels, the offer queue and why makers idle.
func (c Client) HeijunkaState(ctx context.Context) (HeijunkaState, error) {
	var hs HeijunkaState
	_, err := c.do(ctx, http.MethodGet, "/v1/heijunka/state", nil, nil, &hs)
	return hs, err
}
