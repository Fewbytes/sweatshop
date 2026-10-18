package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultEndOfLifeURL is the public endoflife.date API root.
const DefaultEndOfLifeURL = "https://endoflife.date"

// EndOfLife reads runtime release cycles from endoflife.date.
type EndOfLife struct {
	BaseURL string // DefaultEndOfLifeURL unless a test points it elsewhere
	HTTP    *http.Client
}

// FetchProduct returns the raw cycle list for a product slug such as "go".
func (c EndOfLife) FetchProduct(ctx context.Context, product string) ([]byte, error) {
	return httpGet(ctx, c.HTTP, strings.TrimSuffix(c.BaseURL, "/")+"/api/"+url.PathEscape(product)+".json")
}

// Cycle is one release line of a product.
type Cycle struct {
	Name string
	// EOL is the end-of-life date; the zero time means none is scheduled.
	EOL time.Time
	// Ended marks a line already past EOL without a published date.
	Ended bool
}

// IsEOL reports whether the cycle is past end of life at now.
func (c Cycle) IsEOL(now time.Time) bool {
	return c.Ended || (!c.EOL.IsZero() && !now.Before(c.EOL))
}

// ParseCycles decodes an endoflife.date product document. The API's "eol"
// field is a YYYY-MM-DD string once scheduled, false before that, and true
// for lines already ended without a date.
func ParseCycles(payload []byte) ([]Cycle, error) {
	var raw []struct {
		Cycle string          `json:"cycle"`
		EOL   json.RawMessage `json:"eol"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	out := make([]Cycle, 0, len(raw))
	for _, r := range raw {
		c := Cycle{Name: r.Cycle}
		var date string
		if err := json.Unmarshal(r.EOL, &date); err == nil {
			t, err := time.Parse("2006-01-02", date)
			if err != nil {
				return nil, fmt.Errorf("cycle %s: bad eol date %q: %w", r.Cycle, date, err)
			}
			c.EOL = t
		} else {
			switch string(r.EOL) {
			case "true":
				c.Ended = true
			case "false":
			default:
				return nil, fmt.Errorf("cycle %s: unsupported eol value %s", r.Cycle, r.EOL)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// CycleFor finds the cycle containing version: the longest cycle name that
// is a dot-boundary prefix of it ("1.22" for "1.22.3").
func CycleFor(cycles []Cycle, version string) (Cycle, bool) {
	var best Cycle
	found := false
	for _, c := range cycles {
		if version == c.Name || strings.HasPrefix(version, c.Name+".") {
			if !found || len(c.Name) > len(best.Name) {
				best, found = c, true
			}
		}
	}
	return best, found
}
