// Package enrich fetches published truth (latest versions, licenses,
// advisories, deprecation, runtime EOL) from deps.dev and endoflife.date and
// caches it per source with a TTL. Everything here is disposable: losing the
// cache costs one refetch.
package enrich

import (
	"context"
	"fmt"
	"time"
)

// Source names an enrichment origin; it is the cache's first key column.
type Source string

const (
	SourceVersions   Source = "depsdev-versions"
	SourceVersion    Source = "depsdev-version"
	SourceAdvisories Source = "depsdev-advisory"
	SourceEOL        Source = "endoflife"
)

// TTL is how long a cached payload is served without refetching: advisories
// short, versions medium, EOL long (spec: Enrichment cache).
func (s Source) TTL() time.Duration {
	switch s {
	case SourceAdvisories:
		return 6 * time.Hour
	case SourceVersions, SourceVersion:
		return 24 * time.Hour
	case SourceEOL:
		return 7 * 24 * time.Hour
	}
	panic(fmt.Sprintf("enrich: no TTL for source %q", string(s)))
}

// Row is one cached payload.
type Row struct {
	Payload   []byte
	FetchedAt time.Time
}

// Store is the persistence port for the enrichment cache.
type Store interface {
	GetEnrichment(ctx context.Context, source Source, key string) (Row, bool, error)
	PutEnrichment(ctx context.Context, source Source, key string, row Row) error
}

// Cached is a payload served by Fetch.
type Cached struct {
	Payload   []byte
	FetchedAt time.Time
	// RefreshErr is non-nil when the copy is past its TTL and refreshing it
	// failed: the stale copy is served and callers should say so.
	RefreshErr error
}

// Fetch returns the payload for (source, key). A copy younger than the
// source's TTL is served as is. Otherwise fetch runs and its result is
// stored; if fetch fails and a stale copy exists, the stale copy is returned
// with RefreshErr set (offline operation never fails when a copy exists).
// With no copy at all, the fetch error is returned.
func Fetch(ctx context.Context, st Store, now func() time.Time, source Source, key string,
	fetch func(context.Context) ([]byte, error)) (Cached, error) {
	row, found, err := st.GetEnrichment(ctx, source, key)
	if err != nil {
		return Cached{}, err
	}
	if found && now().Sub(row.FetchedAt) < source.TTL() {
		return Cached{Payload: row.Payload, FetchedAt: row.FetchedAt}, nil
	}
	payload, fetchErr := fetch(ctx)
	if fetchErr != nil {
		if found {
			return Cached{Payload: row.Payload, FetchedAt: row.FetchedAt, RefreshErr: fetchErr}, nil
		}
		return Cached{}, fmt.Errorf("fetching %s %s: %w", source, key, fetchErr)
	}
	fresh := Row{Payload: payload, FetchedAt: now()}
	if err := st.PutEnrichment(ctx, source, key, fresh); err != nil {
		return Cached{}, err
	}
	return Cached{Payload: fresh.Payload, FetchedAt: fresh.FetchedAt}, nil
}
