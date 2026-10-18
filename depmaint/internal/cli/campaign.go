package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/depmaint/internal/campaign"
	"github.com/Fewbytes/sweatshop/depmaint/internal/enrich"
	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/planner"
	"github.com/Fewbytes/sweatshop/depmaint/internal/project"
	"github.com/Fewbytes/sweatshop/depmaint/internal/snapshot"
	"github.com/Fewbytes/sweatshop/depmaint/internal/store"
)

// Default campaign budget when `campaign new` is not told otherwise.
const (
	defaultCampaignAttempts = 5
	defaultCampaignDuration = 72 * time.Hour
)

// CampaignNewArgs are the flags of `campaign new`.
type CampaignNewArgs struct {
	Type     campaign.Type
	Target   string // "ecosystem:name=version"
	Attempts int    // 0 = default
}

// openCampaignEngine wires the engine to the cache (graph), deps.dev
// (published truth) and the campaign database. The caller closes the
// returned func.
func openCampaignEngine(ctx context.Context, root string, pub Published, out io.Writer) (campaign.Engine, func(), error) {
	l := Paths(root)
	cache, _, err := openCache(ctx, root)
	if err != nil {
		return campaign.Engine{}, nil, err
	}
	campaigns, err := campaign.OpenStore(ctx, l.Campaigns)
	if err != nil {
		cache.Close()
		return campaign.Engine{}, nil, err
	}
	closeAll := func() { campaigns.Close(); cache.Close() }
	return campaign.Engine{
		Store:    campaigns,
		World:    cacheWorld{store: cache, now: pub.Now},
		Registry: depsDevRegistry{ctx: ctx, pub: pub, cache: cache, out: out},
		Accepts:  planner.SemverAccepts,
		Now:      pub.Now,
	}, closeAll, nil
}

// CampaignNew creates a campaign for target "ecosystem:name=version".
func CampaignNew(ctx context.Context, root string, args CampaignNewArgs, pub Published, out io.Writer) error {
	pkg, version, ok := strings.Cut(args.Target, "=")
	if !ok {
		return fmt.Errorf("target %q: want ecosystem:name=version", args.Target)
	}
	attempts := args.Attempts
	if attempts == 0 {
		attempts = defaultCampaignAttempts
	}
	e, closeAll, err := openCampaignEngine(ctx, root, pub, out)
	if err != nil {
		return err
	}
	defer closeAll()
	c, err := e.New(ctx, campaign.NewSpec{Type: args.Type, Package: pkg, Target: version, Roots: []string{root},
		Budget: campaign.Budget{MaxAttempts: attempts, MaxDuration: defaultCampaignDuration}})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "campaign %s — %s %s → %s\n→ depmaint campaign plan %s\n", c.ID, c.Type, c.Package, c.Target, c.ID)
	return nil
}

// CampaignPlan (re)computes the campaign's blockers and plan and prints them.
func CampaignPlan(ctx context.Context, root, id string, pub Published, out io.Writer) error {
	e, closeAll, err := openCampaignEngine(ctx, root, pub, out)
	if err != nil {
		return err
	}
	defer closeAll()
	c, err := e.Plan(ctx, id, false)
	if err != nil {
		return err
	}
	if c.Plan == nil {
		fmt.Fprintf(out, "campaign %s — %s is already at %s\n", c.ID, c.Package, c.Target)
		return nil
	}
	io.WriteString(out, c.Plan.Render())
	fmt.Fprintf(out, "→ depmaint campaign next %s\n", c.ID)
	return nil
}

// CampaignNext prints the live step and writes its work order under
// .depmaint/work/, or says why there is none.
func CampaignNext(ctx context.Context, root, id string, pub Published, out io.Writer) error {
	e, closeAll, err := openCampaignEngine(ctx, root, pub, out)
	if err != nil {
		return err
	}
	defer closeAll()
	n, err := e.Next(ctx, id)
	if err != nil {
		return err
	}
	c := n.Campaign
	switch n.State {
	case campaign.NextDispatch:
		path := campaign.WorkOrderPath(c.ID, n.Step)
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(n.WorkOrder), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "campaign %s — upgrade %s → %s\nstep %d/%d · work order: %s\n", c.ID, c.Package, c.Target, n.Step, n.Steps, path)
		fmt.Fprintf(out, "→ read %s, execute it, then:\n  depmaint campaign record %s --step %d --verdict <succeeded|rejected|failed> \\\n    --reason \"...\" [--pr <url>]\n",
			path, c.ID, n.Step)
	default:
		fmt.Fprintf(out, "campaign %s — %s", c.ID, n.State)
		if n.Reason != "" {
			fmt.Fprintf(out, ": %s", n.Reason)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// CampaignRecord appends an executor's verdict and prints the next call.
func CampaignRecord(ctx context.Context, root string, in campaign.RecordInput, pub Published, out io.Writer) error {
	e, closeAll, err := openCampaignEngine(ctx, root, pub, out)
	if err != nil {
		return err
	}
	defer closeAll()
	r, err := e.Record(ctx, in)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "recorded %s: step %d %s (%d/%d attempts spent)\n→ depmaint campaign next %s\n",
		r.Attempt.ID, r.Attempt.Step, r.Attempt.Verdict, r.Campaign.Spent, r.Campaign.Budget.MaxAttempts, r.Campaign.ID)
	return nil
}

// CampaignList prints campaigns, optionally only those with status.
func CampaignList(ctx context.Context, root string, status campaign.Status, out io.Writer) error {
	campaigns, err := campaign.OpenStore(ctx, Paths(root).Campaigns)
	if err != nil {
		return err
	}
	defer campaigns.Close()
	list, err := campaigns.List(ctx, status)
	if err != nil {
		return err
	}
	for _, c := range list {
		fmt.Fprintf(out, "%s\t%s\t%s → %s\t%d/%d attempts\n", c.ID, c.Status, c.Package, c.Target, c.Spent, c.Budget.MaxAttempts)
	}
	return nil
}

// cacheWorld serves campaign graphs from the snapshot cache, re-expanding
// the working tree on refresh.
type cacheWorld struct {
	store *store.Store
	now   func() time.Time
}

func (w cacheWorld) Graph(ctx context.Context, roots []string, refresh bool) (planner.Graph, string, error) {
	var graphs []project.RootGraph
	var ids []string
	for _, root := range roots {
		if refresh {
			snap, nodes, edges, err := snapshot.Build(root, expander.All(), w.now())
			if err != nil {
				return planner.Graph{}, "", err
			}
			if err := w.store.InsertSnapshot(ctx, snap, nodes, edges); err != nil {
				return planner.Graph{}, "", err
			}
		}
		snap, ok, err := w.store.LatestSnapshot(ctx, root)
		if err != nil {
			return planner.Graph{}, "", err
		}
		if !ok {
			return planner.Graph{}, "", fmt.Errorf("no snapshot for %s — run `depmaint graph` first", root)
		}
		nodes, err := w.store.SnapshotNodes(ctx, snap.ID)
		if err != nil {
			return planner.Graph{}, "", err
		}
		edges, err := w.store.SnapshotEdges(ctx, snap.ID)
		if err != nil {
			return planner.Graph{}, "", err
		}
		graphs = append(graphs, project.RootGraph{Root: root, Nodes: nodes, Edges: edges})
		ids = append(ids, snap.ID)
	}
	if len(ids) == 1 {
		return planner.MergeRoots(graphs), ids[0], nil
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return planner.MergeRoots(graphs), hex.EncodeToString(sum[:]), nil
}

// depsDevRegistry is published truth from deps.dev, cached. Only npm
// packages are supported: other ecosystems fail loudly rather than plan on
// guessed requirements.
type depsDevRegistry struct {
	ctx   context.Context
	pub   Published
	cache enrich.Store
	out   io.Writer
}

func (r depsDevRegistry) npmName(pkg string) (string, error) {
	name, ok := strings.CutPrefix(pkg, "node:")
	if !ok {
		return "", fmt.Errorf("%s: published requirements are only supported for node packages", pkg)
	}
	return name, nil
}

func (r depsDevRegistry) Versions(pkg string) ([]string, error) {
	name, err := r.npmName(pkg)
	if err != nil {
		return nil, err
	}
	var doc enrich.PackageVersions
	err = fetchDecoded(r.ctx, r.cache, r.pub.Now, r.out, enrich.SourceVersions, enrich.PackageKey("NPM", name),
		func(ctx context.Context) ([]byte, error) { return r.pub.DepsDev.FetchPackageVersions(ctx, "NPM", name) }, &doc)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range doc.Versions {
		out = append(out, v.VersionKey.Version)
	}
	return out, nil
}

func (r depsDevRegistry) Requirements(pkg, version string) ([]planner.Requirement, error) {
	name, err := r.npmName(pkg)
	if err != nil {
		return nil, err
	}
	var doc enrich.Requirements
	err = fetchDecoded(r.ctx, r.cache, r.pub.Now, r.out, enrich.SourceRequires, enrich.VersionKey("NPM", name, version),
		func(ctx context.Context) ([]byte, error) {
			return r.pub.DepsDev.FetchRequirements(ctx, "NPM", name, version)
		}, &doc)
	if err != nil {
		return nil, err
	}
	var out []planner.Requirement
	for _, d := range doc.NPM.Dependencies {
		out = append(out, planner.Requirement{Package: "node:" + d.Name, Constraint: d.Requirement})
	}
	return out, nil
}
