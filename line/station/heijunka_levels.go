package station

import (
	"context"
	"net/http"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

// routeHeijunkaLevels serves the levels the heijunka worker publishes: any
// station may read them (GET), only the heijunka station sets them (POST).
// Actuators that run makers read the levels here; the line database is the
// only copy.
func (s *Server) routeHeijunkaLevels(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no line database")
	}
	switch r.Method {
	case http.MethodGet:
		l, err := s.Store.Levels(r.Context())
		if err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, l)
		return "heijunka levels", nil
	case http.MethodPost:
		if st.Role != heijunkaRole {
			return "", fail(http.StatusForbidden, "role %q may not publish heijunka levels", st.Role)
		}
		var l linestore.Levels
		if err := decodeBody(w, r, &l); err != nil {
			return "", err
		}
		if l.Agents < 0 || l.WIPLimit < 0 {
			return "", fail(http.StatusBadRequest, "levels must not be negative")
		}
		if err := s.Store.SetLevels(r.Context(), l); err != nil {
			return "", err
		}
		writeJSON(w, http.StatusOK, l)
		return "heijunka levels set", nil
	}
	w.Header().Set("Allow", "GET, POST")
	return "", fail(http.StatusMethodNotAllowed, "use GET or POST for %s", r.URL.Path)
}

// HeijunkaLevels returns the levels the heijunka worker last published, zero
// Levels when none were.
func (c Client) HeijunkaLevels(ctx context.Context) (linestore.Levels, error) {
	var l linestore.Levels
	_, err := c.do(ctx, http.MethodGet, "/v1/heijunka/levels", nil, nil, &l)
	return l, err
}

// PublishHeijunkaLevels sets the line's levels; the caller's token must be the
// heijunka station's.
func (c Client) PublishHeijunkaLevels(ctx context.Context, l linestore.Levels) error {
	_, err := c.postJSON(ctx, "/v1/heijunka/levels", l)
	return err
}
