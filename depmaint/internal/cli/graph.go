package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/snapshot"
	"github.com/Fewbytes/sweatshop/depmaint/internal/store"
)

// snapshotsKept is how many snapshots per root PruneSnapshots retains.
const snapshotsKept = 10

// Graph expands the current graph, snapshots it, and reports drift against
// the previous snapshot for root. refresh is accepted for CLI stability
// (spec: `graph --refresh`) but does not currently change behavior: without
// an enrichment staleness window yet in place (that lands with the
// enrichment cache), every graph run re-expands. Once TTL-based staleness
// exists, --refresh will be the flag that bypasses it.
func Graph(ctx context.Context, root string, refresh bool, out io.Writer) error {
	_ = refresh
	l := Paths(root)
	if _, err := os.Stat(l.DB); err != nil {
		return fmt.Errorf("no cache at %s — run `depmaint init` first: %w", l.DB, err)
	}

	s, err := store.Open(l.DB)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		return err
	}

	prevSnap, hasPrev, err := s.LatestSnapshot(ctx, root)
	if err != nil {
		return err
	}
	var prevNodes []expander.Node
	if hasPrev {
		prevNodes, err = s.SnapshotNodes(ctx, prevSnap.ID)
		if err != nil {
			return err
		}
	}

	snap, nodes, edges, err := snapshot.Build(root, expander.All(), time.Now())
	if err != nil {
		return err
	}
	if err := s.InsertSnapshot(ctx, snap, nodes, edges); err != nil {
		return err
	}
	if err := s.PruneSnapshots(ctx, root, snapshotsKept); err != nil {
		return err
	}

	fmt.Fprintf(out, "snapshot %s (%d nodes, %d edges)\n", snap.ID, snap.NodeCount, snap.EdgeCount)
	if !hasPrev {
		fmt.Fprintln(out, "no previous snapshot — nothing to diff")
		return nil
	}
	if prevSnap.ID == snap.ID {
		fmt.Fprintln(out, "no drift — same snapshot as before")
		return nil
	}
	drift := snapshot.Diff(prevNodes, nodes)
	fmt.Fprintf(out, "drift vs %s: +%d nodes, -%d nodes\n", prevSnap.ID, len(drift.AddedNodes), len(drift.RemovedNodes))
	for _, k := range drift.AddedNodes {
		fmt.Fprintf(out, "  + %s\n", k)
	}
	for _, k := range drift.RemovedNodes {
		fmt.Fprintf(out, "  - %s\n", k)
	}
	return nil
}
