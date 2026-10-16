package station

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/Fewbytes/sweatshop/line/report"
)

// routeReports gives the viceroy read-only access to the reports bucket for
// supervision: GET /v1/reports?prefix=P lists keys, GET /v1/reports/object?key=K
// returns one object's bytes. The line performs the reads with its own bucket
// credentials, so no credential reaches the station, and the report.ObjectReader
// interface has no write or delete, so there is nothing to refuse beyond the
// method and the role.
func (s *Server) routeReports(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if st.Role != viceroyRole {
		return "", fail(http.StatusForbidden, "role %q may not read the reports bucket", st.Role)
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		return "", fail(http.StatusMethodNotAllowed, "the reports bucket is read-only: use GET for %s", r.URL.Path)
	}
	reader, ok := s.Reports.(report.ObjectReader)
	if !ok {
		return "", fail(http.StatusServiceUnavailable, "no readable object store")
	}
	switch r.URL.Path {
	case "/v1/reports":
		prefix := r.URL.Query().Get("prefix")
		keys, err := reader.List(r.Context(), prefix)
		if err != nil {
			return "", err
		}
		if keys == nil {
			keys = []string{}
		}
		writeJSON(w, http.StatusOK, keys)
		return "reports list " + prefix, nil
	case "/v1/reports/object":
		key := r.URL.Query().Get("key")
		if key == "" {
			return "", fail(http.StatusBadRequest, "key is required")
		}
		data, err := reader.Get(r.Context(), key)
		if errors.Is(err, report.ErrObjectNotFound) {
			return "", fail(http.StatusNotFound, "no object %q", key)
		}
		if err != nil {
			return "", err
		}
		if len(data) > report.MaxObjectBytes {
			return "", fail(http.StatusRequestEntityTooLarge, "object %q exceeds %d bytes", key, report.MaxObjectBytes)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
		return "reports get " + key, nil
	}
	return "", fail(http.StatusNotFound, "no route %s", r.URL.Path)
}

// ListReports returns the keys in the reports bucket under prefix (viceroy only).
func (c Client) ListReports(ctx context.Context, prefix string) ([]string, error) {
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	var keys []string
	_, err := c.do(ctx, http.MethodGet, "/v1/reports", q, nil, &keys)
	return keys, err
}

// GetReport returns the object at key in the reports bucket (viceroy only).
func (c Client) GetReport(ctx context.Context, key string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, "/v1/reports/object", url.Values{"key": {key}}, nil, nil)
}
