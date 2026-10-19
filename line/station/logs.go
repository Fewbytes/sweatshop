package station

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/Fewbytes/sweatshop/line/linelog"
	"github.com/Fewbytes/sweatshop/line/linestore"
)

const (
	// ServeLogSource is the log source of the station API process itself.
	ServeLogSource = "serve"
	// maxLogPostLines bounds one POST /v1/logs.
	maxLogPostLines = 200
)

// StationTokens lists every station token, for redaction by value.
func (s *Server) StationTokens() []string {
	tokens := make([]string, 0, len(s.Stations))
	for _, st := range s.Stations {
		tokens = append(tokens, st.Token)
	}
	return tokens
}

// RecordLog stores one log line from source, redacting station tokens and
// credential-shaped strings first. A failure is returned, not logged: logging
// it would feed this very path.
func (s *Server) RecordLog(ctx context.Context, source, line string) error {
	if s.Store == nil {
		return nil
	}
	return s.Store.RecordLog(ctx, linestore.LogLine{At: time.Now(), Source: source, Line: linelog.Redact(line, s.StationTokens()...)})
}

// routeLogs serves the line's component logs. Only the viceroy reads them
// (GET ?since=36h&station=NAME, oldest first), redacted again on the way out
// as defence in depth; any station appends lines of its own (POST), recorded
// under its own name. The log is read-only to everyone.
func (s *Server) routeLogs(w http.ResponseWriter, r *http.Request, st *Station) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no line database")
	}
	switch r.Method {
	case http.MethodGet:
		if st.Role != viceroyRole {
			return "", fail(http.StatusForbidden, "role %q may not read line logs", st.Role)
		}
		q := r.URL.Query()
		since, err := sinceQuery(q, time.Now())
		if err != nil {
			return "", err
		}
		f := linestore.LogFilter{Since: since, Source: q.Get("station")}
		if f.Source != "" && !wordRe.MatchString(f.Source) {
			return "", fail(http.StatusBadRequest, "invalid station %q", f.Source)
		}
		lines, err := s.Store.Logs(r.Context(), f)
		if err != nil {
			return "", err
		}
		tokens := s.StationTokens()
		for i := range lines {
			lines[i].Line = linelog.Redact(lines[i].Line, tokens...)
		}
		writeJSON(w, http.StatusOK, lines)
		return "logs", nil
	case http.MethodPost:
		var body struct {
			Lines []string `json:"lines"`
		}
		if err := decodeBody(w, r, &body); err != nil {
			return "", err
		}
		if len(body.Lines) > maxLogPostLines {
			return "", fail(http.StatusRequestEntityTooLarge, "at most %d log lines per request", maxLogPostLines)
		}
		for _, line := range body.Lines {
			if err := s.RecordLog(r.Context(), st.Name, line); err != nil {
				return "", err
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return "logs posted", nil
	}
	w.Header().Set("Allow", "GET, POST")
	return "", fail(http.StatusMethodNotAllowed, "use GET or POST for %s", r.URL.Path)
}

// Logs returns the line's component logs, oldest first; since is a --since
// value ("" for all stored lines). The caller's token must be the viceroy's.
func (c Client) Logs(ctx context.Context, since, station string) ([]linestore.LogLine, error) {
	q := url.Values{}
	for k, v := range map[string]string{"since": since, "station": station} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var lines []linestore.LogLine
	_, err := c.do(ctx, http.MethodGet, "/v1/logs", q, nil, &lines)
	return lines, err
}

// PostLogs appends lines to the line's component logs under the caller's
// station name. At most maxLogPostLines per call.
func (c Client) PostLogs(ctx context.Context, lines []string) error {
	_, err := c.postJSON(ctx, "/v1/logs", map[string][]string{"lines": lines})
	return err
}
