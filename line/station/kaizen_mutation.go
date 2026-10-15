package station

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Fewbytes/sweatshop/line/linestore"
)

// KaizenMutationRuns is the body of GET /v1/kaizen/mutation: every recorded
// mutation measurement, oldest first.
type KaizenMutationRuns struct {
	Runs []linestore.MutationRun `json:"runs"`
}

// mutationValue is a field of mutation.json that is either a value or the
// {"unavailable": reason} object the signal scripts write instead of 0.
type mutationValue[T any] struct {
	Value       *T
	Unavailable string
}

func (v *mutationValue[T]) UnmarshalJSON(data []byte) error {
	var gone struct {
		Unavailable string `json:"unavailable"`
	}
	if json.Unmarshal(data, &gone) == nil && gone.Unavailable != "" {
		v.Unavailable = gone.Unavailable
		return nil
	}
	v.Value = new(T)
	return json.Unmarshal(data, v.Value)
}

// MutationRunFromSignals turns the mutation.json that `kaizen_signals.py
// mutation` wrote into the record the line stores for commit.
func MutationRunFromSignals(commit string, source linestore.MutationSource, mutationJSON []byte) (linestore.MutationRun, error) {
	var sig struct {
		Language    string                 `json:"language"`
		Scope       []string               `json:"scope"`
		Tool        string                 `json:"tool"`
		ToolVersion mutationValue[string]  `json:"tool_version"`
		Score       mutationValue[float64] `json:"mutation_score"`
		SampleSize  mutationValue[int]     `json:"sample_size"`
		DurationMS  *int64                 `json:"duration_ms"`
	}
	if err := json.Unmarshal(mutationJSON, &sig); err != nil {
		return linestore.MutationRun{}, fmt.Errorf("decode mutation.json: %w", err)
	}
	if sig.DurationMS == nil {
		return linestore.MutationRun{}, fmt.Errorf("mutation.json has no duration_ms: it was written by an older kaizen_signals.py")
	}
	m := linestore.MutationRun{
		Commit: commit, Source: source, Language: sig.Language, Scope: sig.Scope, Tool: sig.Tool,
		DurationMS: *sig.DurationMS, SampleSize: sig.SampleSize.Value, Score: sig.Score.Value, Unavailable: sig.Score.Unavailable,
	}
	if sig.ToolVersion.Value != nil {
		m.ToolVersion = *sig.ToolVersion.Value
	}
	return m, nil
}

// recordKaizenMutation serves POST /v1/kaizen/mutation: it stores one mutation
// measurement. A measurement has either a score with its sample size or the
// reason it is unavailable, never both and never neither.
func (s *Server) recordKaizenMutation(w http.ResponseWriter, r *http.Request) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no signals history configured")
	}
	var m linestore.MutationRun
	if err := decodeBody(w, r, &m); err != nil {
		return "", err
	}
	switch {
	case !fullHex.MatchString(m.Commit):
		return "", fail(http.StatusBadRequest, "commit %q is not a full lowercase hex object id", m.Commit)
	case m.Source != linestore.MutationFromKaizenRun && m.Source != linestore.MutationFromLandedCard:
		return "", fail(http.StatusBadRequest, "source %q is neither %q nor %q", m.Source, linestore.MutationFromKaizenRun, linestore.MutationFromLandedCard)
	case m.Language == "" || m.Tool == "" || len(m.Scope) == 0:
		return "", fail(http.StatusBadRequest, "language, tool and scope are required")
	case m.DurationMS < 0:
		return "", fail(http.StatusBadRequest, "duration_ms is negative")
	case (m.Score == nil) != (m.SampleSize == nil):
		return "", fail(http.StatusBadRequest, "score and sample_size go together")
	case (m.Score == nil) == (m.Unavailable == ""):
		return "", fail(http.StatusBadRequest, "give either a score or the reason it is unavailable")
	case m.Score != nil && (*m.Score < 0 || *m.Score > 1):
		return "", fail(http.StatusBadRequest, "score %g is outside [0, 1]", *m.Score)
	}
	if err := s.Store.RecordMutationRun(r.Context(), m); err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, map[string]string{"commit": m.Commit})
	return "kaizen mutation " + m.Commit, nil
}

// listKaizenMutation serves GET /v1/kaizen/mutation.
func (s *Server) listKaizenMutation(w http.ResponseWriter, r *http.Request) (string, error) {
	if s.Store == nil {
		return "", fail(http.StatusServiceUnavailable, "no signals history configured")
	}
	runs, err := s.Store.MutationRuns(r.Context())
	if err != nil {
		return "", err
	}
	writeJSON(w, http.StatusOK, KaizenMutationRuns{Runs: runs})
	return "kaizen mutation runs", nil
}

// RecordKaizenMutation stores one mutation measurement; the caller's token
// must be a kaizen station's.
func (c Client) RecordKaizenMutation(ctx context.Context, m linestore.MutationRun) error {
	_, err := c.postJSON(ctx, "/v1/kaizen/mutation", m)
	return err
}

// KaizenMutationRuns returns every recorded mutation measurement, oldest first.
func (c Client) KaizenMutationRuns(ctx context.Context) ([]linestore.MutationRun, error) {
	var res KaizenMutationRuns
	_, err := c.do(ctx, http.MethodGet, "/v1/kaizen/mutation", nil, nil, &res)
	return res.Runs, err
}
