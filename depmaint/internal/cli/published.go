package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/enrich"
	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/store"
)

// Published bundles the enrichment sources `outdated` and `audit` read.
// Tests point the clients at httptest servers and inject Now.
type Published struct {
	DepsDev   enrich.DepsDev
	EndOfLife enrich.EndOfLife
	Now       func() time.Time
}

// DefaultPublished talks to the public deps.dev and endoflife.date APIs.
func DefaultPublished() Published {
	client := &http.Client{Timeout: 30 * time.Second}
	return Published{
		DepsDev:   enrich.DepsDev{BaseURL: enrich.DefaultDepsDevURL, HTTP: client},
		EndOfLife: enrich.EndOfLife{BaseURL: enrich.DefaultEndOfLifeURL, HTTP: client},
		Now:       time.Now,
	}
}

// nodeRef is a parsed "ecosystem:name@version" node key.
type nodeRef struct {
	Key, Ecosystem, Name, Version string
}

func parseNodeKey(key string) (nodeRef, error) {
	eco, rest, ok := strings.Cut(key, ":")
	at := strings.LastIndex(rest, "@")
	if !ok || at <= 0 {
		return nodeRef{}, fmt.Errorf("malformed node key %q (want ecosystem:name@version)", key)
	}
	return nodeRef{Key: key, Ecosystem: eco, Name: rest[:at], Version: rest[at+1:]}, nil
}

// openCache opens the initialized cache for root and returns the nodes of
// its latest snapshot.
func openCache(ctx context.Context, root string) (*store.Store, []expander.Node, error) {
	l := Paths(root)
	if _, err := os.Stat(l.DB); err != nil {
		return nil, nil, fmt.Errorf("no cache at %s — run `depmaint init` first: %w", l.DB, err)
	}
	s, err := store.Open(l.DB)
	if err != nil {
		return nil, nil, err
	}
	if err := s.Migrate(ctx); err != nil {
		s.Close()
		return nil, nil, err
	}
	snap, ok, err := s.LatestSnapshot(ctx, root)
	if err == nil && !ok {
		err = fmt.Errorf("no snapshot for %s — run `depmaint graph` first", root)
	}
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	nodes, err := s.SnapshotNodes(ctx, snap.ID)
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	return s, nodes, nil
}

// noteStale tells the user when a served copy is past its TTL because its
// refresh failed.
func noteStale(out io.Writer, source enrich.Source, key string, c enrich.Cached) {
	if c.RefreshErr != nil {
		fmt.Fprintf(out, "note: %s %s served from cache fetched %s (refresh failed: %v)\n",
			source, key, c.FetchedAt.Format(time.RFC3339), c.RefreshErr)
	}
}

// fetchDecoded serves a cached-or-fetched payload decoded into v.
func fetchDecoded(ctx context.Context, st enrich.Store, now func() time.Time, out io.Writer,
	source enrich.Source, key string, fetch func(context.Context) ([]byte, error), v any) error {
	c, err := enrich.Fetch(ctx, st, now, source, key, fetch)
	if err != nil {
		return err
	}
	noteStale(out, source, key, c)
	return enrich.Decode(c.Payload, v)
}
