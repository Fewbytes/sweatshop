// Package snapshot builds a content-addressed graph snapshot from whatever
// expanders are available, and diffs node sets for drift reporting. Pure
// functions only — no I/O, no SQL.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
)

// Snapshot is one immutable, content-addressed expansion of a root's graph.
type Snapshot struct {
	ID        string // hex sha256 of the canonicalized node+edge set
	Root      string
	CreatedAt string // RFC3339
	Expanders string // comma-joined names of expanders that ran, sorted
	NodeCount int
	EdgeCount int
}

// Drift is the node-level difference between two snapshots of the same root.
type Drift struct {
	AddedNodes   []string
	RemovedNodes []string
}

// Build runs every available expander against root, merges their output,
// and computes the snapshot's content hash. Unavailable expanders are
// skipped, never an error (spec: "missing toolchain -> warn, skip"). The
// same root and the same expander output always produce the same ID,
// regardless of registration order or wall-clock time.
func Build(root string, expanders []expander.Expander, now time.Time) (Snapshot, []expander.Node, []expander.Edge, error) {
	var nodes []expander.Node
	var edges []expander.Edge
	var used []string
	for _, e := range expanders {
		if !e.Available() {
			continue
		}
		n, ed, err := e.Expand(root)
		if err != nil {
			return Snapshot{}, nil, nil, err
		}
		nodes = append(nodes, n...)
		edges = append(edges, ed...)
		used = append(used, e.Name())
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Key < nodes[j].Key })
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Requirement < edges[j].Requirement
	})
	sort.Strings(used)

	id := hash(nodes, edges)

	return Snapshot{
		ID:        id,
		Root:      root,
		CreatedAt: now.UTC().Format(time.RFC3339),
		Expanders: strings.Join(used, ","),
		NodeCount: len(nodes),
		EdgeCount: len(edges),
	}, nodes, edges, nil
}

// hash computes the content address of a sorted node/edge set. Callers must
// sort nodes by Key and edges by (From, To, Requirement) first.
func hash(nodes []expander.Node, edges []expander.Edge) string {
	h := sha256.New()
	for _, n := range nodes {
		h.Write([]byte("N\x1f" + n.Key + "\x1f" + n.Kind + "\x1f" + strconv.FormatBool(n.Direct) + "\x1e"))
	}
	for _, e := range edges {
		h.Write([]byte("E\x1f" + e.From + "\x1f" + e.To + "\x1f" + e.Requirement + "\x1f" +
			e.Resolved + "\x1f" + strconv.FormatBool(e.Internal) + "\x1e"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Diff reports which node keys appear in curr but not prev, and vice versa.
func Diff(prev, curr []expander.Node) Drift {
	prevKeys := make(map[string]bool, len(prev))
	for _, n := range prev {
		prevKeys[n.Key] = true
	}
	currKeys := make(map[string]bool, len(curr))
	for _, n := range curr {
		currKeys[n.Key] = true
	}
	var d Drift
	for k := range currKeys {
		if !prevKeys[k] {
			d.AddedNodes = append(d.AddedNodes, k)
		}
	}
	for k := range prevKeys {
		if !currKeys[k] {
			d.RemovedNodes = append(d.RemovedNodes, k)
		}
	}
	sort.Strings(d.AddedNodes)
	sort.Strings(d.RemovedNodes)
	return d
}
