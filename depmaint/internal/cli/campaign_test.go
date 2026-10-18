package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/depmaint/internal/campaign"
	"github.com/Fewbytes/sweatshop/depmaint/internal/expander"
	"github.com/Fewbytes/sweatshop/depmaint/internal/snapshot"
	"github.com/Fewbytes/sweatshop/depmaint/internal/store"
)

// campaignWorld is a published world whose cached snapshot has the edges a
// plug-blocks-lib upgrade needs.
func campaignWorld(t *testing.T) *publishedWorld {
	t.Helper()
	routes := map[string]string{
		"/v3/systems/NPM/packages/lib":                                   `{"versions":[{"versionKey":{"version":"1.0.0"}},{"versionKey":{"version":"1.2.0"}}]}`,
		"/v3/systems/NPM/packages/plug":                                  `{"versions":[{"versionKey":{"version":"1.0.0"}},{"versionKey":{"version":"1.1.0"}}]}`,
		"/v3alpha/systems/NPM/packages/lib/versions/1.2.0:requirements":  `{"npm":{}}`,
		"/v3alpha/systems/NPM/packages/plug/versions/1.1.0:requirements": `{"npm":{"dependencies":[{"name":"lib","requirement":"^1.0.0"}]}}`,
	}
	nodes := []expander.Node{
		{Key: "node:app@1.0.0", Kind: "package"}, {Key: "node:lib@1.0.0", Kind: "package", Direct: true},
		{Key: "node:plug@1.0.0", Kind: "package", Direct: true},
	}
	w := newPublishedWorld(t, routes, nodes...)
	edges := []expander.Edge{
		{From: "node:app@1.0.0", To: "node:lib@1.0.0", Requirement: "^1.0.0"},
		{From: "node:app@1.0.0", To: "node:plug@1.0.0", Requirement: ">=1.0.0"},
		{From: "node:plug@1.0.0", To: "node:lib@1.0.0", Requirement: "~1.0.0"},
	}
	s, err := store.Open(Paths(w.root).DB)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap := snapshot.Snapshot{ID: "snap-2", Root: w.root, CreatedAt: w.now.Format("2006-01-02T15:04:05Z07:00"), Expanders: "fake", NodeCount: len(nodes), EdgeCount: len(edges)}
	if err := s.InsertSnapshot(context.Background(), snap, nodes, edges); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestCampaignCommandsRunTheNextRecordLoopAndRememberRejections(t *testing.T) {
	w := campaignWorld(t)
	ctx, pub := context.Background(), w.published()
	run := func(f func(out *bytes.Buffer) error) string {
		t.Helper()
		var out bytes.Buffer
		if err := f(&out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	requireContains(t, run(func(o *bytes.Buffer) error {
		return CampaignNew(ctx, w.root, CampaignNewArgs{Type: campaign.TypeUpgrade, Target: "node:lib=1.2.0"}, pub, o)
	}), "depmaint campaign plan dep_c1")
	requireContains(t, run(func(o *bytes.Buffer) error { return CampaignPlan(ctx, w.root, "dep_c1", pub, o) }),
		"step 1: node:plug 1.0.0 -> 1.1.0")

	next := run(func(o *bytes.Buffer) error { return CampaignNext(ctx, w.root, "dep_c1", pub, o) })
	requireContains(t, next, "step 1/2 · work order: .depmaint/work/dep_c1-001.md")
	requireContains(t, next, "depmaint campaign record dep_c1 --step 1")
	order, err := os.ReadFile(filepath.Join(w.root, ".depmaint/work/dep_c1-001.md"))
	if err != nil {
		t.Fatal(err)
	}
	requireContains(t, string(order), "move node:plug from 1.0.0 to 1.1.0")

	requireContains(t, run(func(o *bytes.Buffer) error {
		return CampaignRecord(ctx, w.root, campaign.RecordInput{CampaignID: "dep_c1", Step: 1,
			Verdict: campaign.VerdictRejected, Reason: "hooks API removed", Executor: "agent-a"}, pub, o)
	}), "step 1 rejected")
	blocked := run(func(o *bytes.Buffer) error { return CampaignNext(ctx, w.root, "dep_c1", pub, o) })
	requireContains(t, blocked, "blocked")
	requireContains(t, blocked, "hooks API removed")

	requireContains(t, run(func(o *bytes.Buffer) error { return CampaignList(ctx, w.root, "", o) }),
		"dep_c1\trunning\tnode:lib → 1.2.0\t1/5 attempts")
}
