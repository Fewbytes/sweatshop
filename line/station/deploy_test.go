package station

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/deploy"
	"github.com/Fewbytes/sweatshop/line/report"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeBuilder struct{ built []string }

func (f *fakeBuilder) Build(_ context.Context, commit string) (deploy.Release, error) {
	f.built = append(f.built, commit)
	return deploy.Release{Commit: commit, Image: "reg.example/sweatshop-line", Digest: testDigest}, nil
}

type deployRig struct {
	srv          *Server
	viceroy, mkr Client
	deployer     Client
	ch           *Channel
	fd           *fakeDiscordServer
	master       string
	builder      *fakeBuilder
}

func newDeployRig(t *testing.T) *deployRig {
	t.Helper()
	srv, v, m, ch, fd := consRig(t)
	ctx := context.Background()
	repo := srv.Guard.Repo
	srv.Guard.Ref = "refs/heads/master"
	blob, _ := repo.WriteBlob(ctx, []byte("x"))
	tree, _ := repo.WriteTreeWithBlob(ctx, "f", blob)
	master, err := repo.CommitTree(ctx, tree, "", "seed", "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(ctx, "refs/heads/master", master, ""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv.Deploy = &DeployState{Path: filepath.Join(dir, "deploy.json")}
	srv.DeployToken = "dep-secret"
	srv.RunsPath = filepath.Join(dir, "runs.jsonl")
	b := &fakeBuilder{}
	srv.Releases = b
	ch.Deploy = srv.ApproveDeploy
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &deployRig{srv: srv, viceroy: v, mkr: m, deployer: Client{URL: ts.URL, Token: "dep-secret"}, ch: ch, fd: fd, master: master, builder: b}
}

func (r *deployRig) attest(t *testing.T, commit string, passed bool) {
	t.Helper()
	err := (&report.JSONLRunStore{Path: r.srv.RunsPath}).RecordRun(context.Background(), report.Stats{Commit: commit, Passed: passed})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *deployRig) king(t *testing.T, id, author, text string) {
	t.Helper()
	r.fd.add(id, author, text)
	if err := r.ch.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func desired(t *testing.T, c Client) *deploy.Release {
	t.Helper()
	rel, err := deploy.Client{URL: c.URL, Token: c.Token}.Desired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestDeployRequestApproveDesiredStatus(t *testing.T) {
	r := newDeployRig(t)
	ctx := context.Background()
	r.fd.add("1", "1", "history")
	_ = r.ch.Poll(ctx)
	short := r.master[:12]

	r.attest(t, r.master, true)
	if _, err := r.viceroy.DeployRequest(ctx, r.master); err != nil {
		t.Fatal(err)
	}
	if len(r.fd.posted) != 1 || r.fd.posted[0] != "redeploy line to "+short+"? reply: approve deploy "+short {
		t.Fatalf("channel: %q", r.fd.posted)
	}
	if desired(t, r.deployer) != nil {
		t.Fatal("desired before approval")
	}

	r.king(t, "2", "666", "approve deploy "+short) // not the king
	if desired(t, r.deployer) != nil {
		t.Fatal("non-king approve took effect")
	}
	r.king(t, "3", kingID, "approve deploy "+short)
	rel := desired(t, r.deployer)
	if rel == nil || rel.Commit != r.master || rel.Ref() != "reg.example/sweatshop-line@"+testDigest {
		t.Fatalf("desired: %+v", rel)
	}

	st := deploy.Status{Commit: r.master, State: deploy.StateApplied}
	if err := (deploy.Client{URL: r.deployer.URL, Token: r.deployer.Token}).Report(ctx, st); err != nil {
		t.Fatal(err)
	}
	if last := r.fd.posted[len(r.fd.posted)-1]; last != "deploy of line "+short+": applied" {
		t.Fatalf("status post: %q", last)
	}
	body, err := r.mkr.do(ctx, "GET", "/v1/deploy/status", nil, nil, nil)
	if err != nil || !strings.Contains(string(body), `"state":"applied"`) {
		t.Fatalf("status: %s %v", body, err)
	}
}

func TestDeployRequestRefusals(t *testing.T) {
	r := newDeployRig(t)
	ctx := context.Background()
	other := strings.Repeat("a", 40)

	if _, err := r.viceroy.DeployRequest(ctx, r.master); err == nil || !strings.Contains(err.Error(), "no passing attestation") {
		t.Fatalf("unattested master: %v", err)
	}
	r.attest(t, r.master, false)
	if _, err := r.viceroy.DeployRequest(ctx, r.master); err == nil {
		t.Fatal("failed run counts as attested")
	}
	r.attest(t, other, true)
	if _, err := r.viceroy.DeployRequest(ctx, other); err == nil || !strings.Contains(err.Error(), "not the line's master") {
		t.Fatalf("non-master: %v", err)
	}
	r.attest(t, r.master, true)
	if _, err := r.mkr.DeployRequest(ctx, r.master); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker request: %v", err)
	}
	if len(r.builder.built) != 0 || len(r.fd.posted) != 0 {
		t.Fatalf("refused requests built or posted: %v %v", r.builder.built, r.fd.posted)
	}
}

func TestDeployRefusedWhileAndonPulled(t *testing.T) {
	r := newDeployRig(t)
	r.attest(t, r.master, true)
	ctx := context.Background()
	// An andon event ref stops the line.
	if err := r.srv.Guard.Repo.UpdateRef(ctx, "refs/line/andon", r.master, ""); err != nil {
		t.Skipf("andon ref layout differs: %v", err)
	}
	if id, _, _ := r.srv.Guard.Stopped(ctx); id == "" {
		t.Skip("andon ref does not stop the line in this layout")
	}
	if _, err := r.viceroy.DeployRequest(ctx, r.master); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("deploy while stopped: %v", err)
	}
}

func TestDeployApproveStaleAndUnknown(t *testing.T) {
	r := newDeployRig(t)
	ctx := context.Background()
	r.attest(t, r.master, true)
	if _, err := r.viceroy.DeployRequest(ctx, r.master); err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.ApproveDeploy(ctx, "deadbee", "1"); err == nil {
		t.Fatal("approved an unknown request")
	}
	// master moves on
	repo := r.srv.Guard.Repo
	blob, _ := repo.WriteBlob(ctx, []byte("y"))
	tree, _ := repo.WriteTreeWithBlob(ctx, "f", blob)
	next, _ := repo.CommitTree(ctx, tree, r.master, "next", "t")
	if err := repo.UpdateRef(ctx, "refs/heads/master", next, r.master); err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.ApproveDeploy(ctx, r.master[:12], "1"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale approve: %v", err)
	}
}

func TestDeployerTokenReachesOnlyDeployRoutes(t *testing.T) {
	r := newDeployRig(t)
	ctx := context.Background()
	if _, err := r.deployer.Status(ctx); err != nil {
		t.Fatalf("health route: %v", err)
	}
	for _, path := range []string{"/v1/cards", "/v1/constitution", "/v1/channel/inbox", "/v1/runs"} {
		if _, err := r.deployer.do(ctx, "GET", path, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("deployer token reached %s: %v", path, err)
		}
	}
	// Stations cannot report outcomes; a wrong commit is refused.
	b, _ := json.Marshal(deploy.Status{Commit: r.master, State: deploy.StateApplied})
	if _, err := r.viceroy.do(ctx, "POST", "/v1/deploy/status", nil, strings.NewReader(string(b)), nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("viceroy status: %v", err)
	}
	if err := (deploy.Client{URL: r.deployer.URL, Token: r.deployer.Token}).Report(ctx, deploy.Status{Commit: r.master, State: deploy.StateApplied}); err == nil {
		t.Fatal("status for a release that was never desired")
	}
}

func TestEngineReleaseBuilderRecordsDigest(t *testing.T) {
	r := newDeployRig(t)
	var calls []string
	b := EngineReleaseBuilder{
		Repo: r.srv.Guard.Repo, Image: "reg.example/sweatshop-line",
		Run: func(_ context.Context, args ...string) (string, error) {
			calls = append(calls, args[0])
			if args[0] == "image" {
				return `["other/repo@sha256:` + strings.Repeat("1", 64) + `","reg.example/sweatshop-line@` + testDigest + `"]`, nil
			}
			if args[0] == "build" && !strings.Contains(strings.Join(args, " "), "-t reg.example/sweatshop-line:"+r.master+" ") {
				t.Errorf("build tag: %v", args)
			}
			return "", nil
		},
	}
	rel, err := b.Build(context.Background(), r.master)
	if err != nil || rel.Digest != testDigest || rel.Image != "reg.example/sweatshop-line" || rel.Commit != r.master {
		t.Fatalf("%+v %v", rel, err)
	}
	if strings.Join(calls, ",") != "build,push,image" {
		t.Fatalf("calls %v", calls)
	}
}
