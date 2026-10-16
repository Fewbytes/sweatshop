package station

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/report"
)

// routeReports gives the viceroy read-only access to the reports bucket for
// supervision: GET /v1/reports?prefix=P lists keys, GET /v1/reports/object?key=K
// returns one object's bytes, GET /v1/reports/presign?key=K&ttl=D returns a
// time-limited read-only URL for a kaizen report. The line performs the reads with its own bucket
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
	if r.URL.Path == "/v1/reports/presign" {
		return s.presignReport(w, r)
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

// presignableKaizenPrefixes are the key prefixes of kaizen reports: analyses
// (KaizenAnalysisKey) and signals (KaizenSignalsPrefix). Only these may be
// linked to, so a link never exposes CI run output or other bucket content.
var presignableKaizenPrefixes = []string{"kaizen/", "reports/kaizen/"}

// presignReport serves GET /v1/reports/presign. A ttl above the
// constitution's viceroy.presign_max_ttl is refused, not clamped, so the
// viceroy never hands the king a link that lives shorter than it asked for.
func (s *Server) presignReport(w http.ResponseWriter, r *http.Request) (string, error) {
	presigner, ok := s.Reports.(report.ObjectPresigner)
	if !ok {
		return "", fail(http.StatusServiceUnavailable, "the object store cannot presign links")
	}
	key := r.URL.Query().Get("key")
	if !slices.ContainsFunc(presignableKaizenPrefixes, func(p string) bool { return strings.HasPrefix(key, p) }) ||
		slices.Contains(strings.Split(key, "/"), "..") {
		return "", fail(http.StatusBadRequest, "key %q is not a kaizen report (want a key under %s)", key, strings.Join(presignableKaizenPrefixes, " or "))
	}
	maxTTL := s.Guard.Constitution.Viceroy.PresignMaxTTLOrDefault()
	ttl := maxTTL
	if raw := r.URL.Query().Get("ttl"); raw != "" {
		var err error
		if ttl, err = time.ParseDuration(raw); err != nil || ttl < time.Second {
			return "", fail(http.StatusBadRequest, "ttl %q: want a duration of at least 1s like 15m", raw)
		}
	}
	if ttl > maxTTL {
		return "", fail(http.StatusBadRequest, "ttl %s exceeds the constitution's viceroy.presign_max_ttl %s", ttl, maxTTL)
	}
	link, err := presigner.PresignGet(key, ttl)
	if err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, PresignedReport{URL: link, TTL: ttl.String()})
	return "reports presign " + key, nil
}

// PresignedReport is the reply to GET /v1/reports/presign.
type PresignedReport struct {
	URL string `json:"url"`
	TTL string `json:"ttl"`
}

// PresignReport returns a read-only link to the kaizen report at key that
// lives ttl (the constitution's cap when zero; viceroy only).
func (c Client) PresignReport(ctx context.Context, key string, ttl time.Duration) (PresignedReport, error) {
	q := url.Values{"key": {key}}
	if ttl != 0 {
		q.Set("ttl", ttl.String())
	}
	var res PresignedReport
	_, err := c.do(ctx, http.MethodGet, "/v1/reports/presign", q, nil, &res)
	return res, err
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
