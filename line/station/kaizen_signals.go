package station

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

// maxKaizenSignalsBody bounds a signals publish: signals.json grows with the
// package count, well past the card routes' 64 KiB limit.
const maxKaizenSignalsBody = 8 << 20

// KaizenSignals is the body of POST /v1/kaizen/signals: one run's measured
// code-health signals (signals.json) and its status report (report.html),
// stored with the CI reports of the commit they were measured at. Commit is
// the master commit the baseline was measured on. After is the signals.json
// re-measured on the changed tree; a run that publishes it again after its
// change overwrites the report and adds signals-after.json under the same key.
type KaizenSignals struct {
	Commit     string          `json:"commit"`
	Signals    json.RawMessage `json:"signals"`
	After      json.RawMessage `json:"after,omitempty"`
	ReportHTML string          `json:"report_html"`
}

// KaizenSignalsRecord is one stored baseline measurement, the line's history
// of code health that the next run compares itself with.
type KaizenSignalsRecord struct {
	Commit  string          `json:"commit"`
	Signals json.RawMessage `json:"signals"`
}

// KaizenPreviousSignals is the body of GET /v1/kaizen/signals/previous;
// Previous is nil when the line has no earlier baseline.
type KaizenPreviousSignals struct {
	Previous *KaizenSignalsRecord `json:"previous"`
}

// KaizenSignalsPrefix is the object-store key prefix of a commit's Kaizen
// signals, next to the witness reports under reports/<commit>/.
func KaizenSignalsPrefix(commit string) string { return "reports/kaizen/" + commit }

type kaizenObject struct {
	contentType string
	data        []byte
}

func (s *Server) publishKaizenSignals(w http.ResponseWriter, r *http.Request) (string, error) {
	if s.Reports == nil || s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no object store or signals history configured")
	}
	var k KaizenSignals
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxKaizenSignalsBody)).Decode(&k); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return "", fail(http.StatusRequestEntityTooLarge, "body exceeds %d bytes", maxKaizenSignalsBody)
		}
		return "", fail(http.StatusBadRequest, "decode body: %v", err)
	}
	if !fullHex.MatchString(k.Commit) {
		return "", fail(http.StatusBadRequest, "commit %q is not a full lowercase hex object id", k.Commit)
	}
	for name, doc := range map[string]json.RawMessage{"signals": k.Signals, "after": k.After} {
		if name == "after" && len(k.After) == 0 {
			continue
		}
		var obj map[string]any
		if json.Unmarshal(doc, &obj) != nil || obj == nil {
			return "", fail(http.StatusBadRequest, "%s is not a JSON object", name)
		}
	}
	if k.ReportHTML == "" {
		return "", fail(http.StatusBadRequest, "report_html is empty")
	}
	prefix := KaizenSignalsPrefix(k.Commit)
	objects := map[string]kaizenObject{
		"signals.json": {"application/json", k.Signals},
		"report.html":  {"text/html; charset=utf-8", []byte(k.ReportHTML)},
	}
	if len(k.After) > 0 {
		objects["signals-after.json"] = kaizenObject{"application/json", k.After}
	}
	for name, o := range objects {
		if err := s.Reports.Put(r.Context(), prefix+"/"+name, o.contentType, o.data); err != nil {
			return "", err
		}
	}
	if err := s.Store.AddKaizenBaseline(r.Context(), linestore.KaizenBaseline{Commit: k.Commit, Signals: bytes.Clone(k.Signals)}); err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": prefix})
	return "kaizen signals " + prefix, nil
}

// previousKaizenSignals serves GET /v1/kaizen/signals/previous?commit=: the
// newest stored baseline measured at a commit other than the given one.
func (s *Server) previousKaizenSignals(w http.ResponseWriter, r *http.Request) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no signals history configured")
	}
	commit, err := commitParam(r)
	if err != nil {
		return "", err
	}
	prev, err := s.Store.PreviousKaizenBaseline(r.Context(), commit)
	if err != nil {
		return "", err
	}
	var res KaizenPreviousSignals
	if prev != nil {
		res.Previous = &KaizenSignalsRecord{Commit: prev.Commit, Signals: prev.Signals}
	}
	writeJSON(w, http.StatusOK, res)
	return "kaizen previous signals", nil
}

// PublishKaizenSignals stores a run's signals and status report under
// reports/kaizen/<commit>/ and returns that prefix; the caller's token must
// be a kaizen station's.
func (c Client) PublishKaizenSignals(ctx context.Context, k KaizenSignals) (string, error) {
	return c.postJSONForKey(ctx, "/v1/kaizen/signals", k)
}

// PreviousKaizenSignals returns the newest stored baseline measured at a
// commit other than the given one; nil when the line has none.
func (c Client) PreviousKaizenSignals(ctx context.Context, commit string) (*KaizenSignalsRecord, error) {
	var res KaizenPreviousSignals
	_, err := c.do(ctx, http.MethodGet, "/v1/kaizen/signals/previous", url.Values{"commit": {commit}}, nil, &res)
	return res.Previous, err
}

// KaizenRuns returns the CI metrics store's newest passing runs, which the
// signal scripts turn into the test-runtime signal.
func (c Client) KaizenRuns(ctx context.Context) (RunsResponse, error) {
	var res RunsResponse
	_, err := c.do(ctx, http.MethodGet, "/v1/runs", url.Values{"passed": {"true"}, "limit": {"50"}}, nil, &res)
	return res, err
}
