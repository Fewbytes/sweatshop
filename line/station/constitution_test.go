package station

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
)

const baseCons = "[trust]\nlevel = \"none\"\n\n[[checks.required]]\nname = \"ci\"\ncommand = \"true\"\n\n[runner]\nkind = \"local\"\n"

func consRig(t *testing.T) (*Server, Client, Client, *Channel, *fakeDiscordServer) {
	t.Helper()
	ctx := context.Background()
	repo := gitx.Repo{Dir: t.TempDir()}
	if _, err := repo.Git(ctx, "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "constitution.toml")
	if err := os.WriteFile(file, []byte(baseCons), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConstitution(ctx, repo, file); err != nil {
		t.Fatal(err)
	}
	ch, fd := newChan(t, nil)
	srv := &Server{Stations: []*Station{{Name: "v", Role: "viceroy", Token: "vt"}, {Name: "m", Role: "maker", Token: "mt"}}, Channel: ch}
	srv.Guard.Repo = repo
	ch.Constitution = srv.ApproveConstitution
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, Client{URL: ts.URL, Token: "vt"}, Client{URL: ts.URL, Token: "mt"}, ch, fd
}

func TestLoadConstitutionImportsOnceThenReadsRef(t *testing.T) {
	ctx := context.Background()
	repo := gitx.Repo{Dir: t.TempDir()}
	if _, err := repo.Git(ctx, "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(file, []byte(baseCons), 0o644)
	_, d1, err := LoadConstitution(ctx, repo, file)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(file, []byte("garbage ="), 0o644) // the file is no longer consulted
	_, d2, err := LoadConstitution(ctx, repo, file)
	if err != nil || d1 != d2 {
		t.Fatalf("reload: %v %s %s", err, d1, d2)
	}
	if ok, _ := repo.RefExists(ctx, ConstitutionRef); !ok {
		t.Fatal("ref not created")
	}
}

func TestConstitutionProposeApproveAndHistory(t *testing.T) {
	srv, v, m, ch, fd := consRig(t)
	ctx := context.Background()
	next := baseCons + "\n[viceroy]\nsuccession = true\n"

	if _, err := v.ConstitutionPropose(ctx, []byte("not = [toml")); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("parse failure accepted: %v", err)
	}
	if _, err := m.ConstitutionPropose(ctx, []byte(next)); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker proposal: %v", err)
	}
	n, err := v.ConstitutionPropose(ctx, []byte(next))
	if err != nil || n != 1 {
		t.Fatalf("propose: %d %v", n, err)
	}
	if len(fd.posted) != 1 || !strings.Contains(fd.posted[0], "+ succession = true") || !strings.Contains(fd.posted[0], "approve constitution 1") {
		t.Fatalf("diff post: %q", fd.posted)
	}

	fd.add("1", "5", "x")
	_ = ch.Poll(ctx)
	// A non-king approval is only data.
	fd.add("2", "6", "approve constitution 1")
	_ = ch.Poll(ctx)
	if got, _ := v.ConstitutionGet(ctx); string(got) != baseCons {
		t.Fatalf("non-king approve applied: %q", got)
	}
	restarted := false
	ch.Restart = func() { restarted = true }
	fd.add("3", kingID, "approve constitution 1")
	_ = ch.Poll(ctx)
	if got, _ := v.ConstitutionGet(ctx); string(got) != next {
		t.Fatalf("king approve not applied: %q", got)
	}
	if !restarted {
		t.Error("no reload requested")
	}

	entries, err := m.ConstitutionLog(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("log %v %v", entries, err)
	}
	if !strings.Contains(entries[0].Message, "by v") || !strings.Contains(entries[0].Message, "king message 3") {
		t.Errorf("message %q", entries[0].Message)
	}
	old, err := srv.Guard.Repo.ReadBlob(ctx, entries[1].Commit+":constitution")
	if err != nil || string(old) != baseCons {
		t.Fatalf("history %q %v", old, err)
	}

	// A proposal made against an older head is stale.
	if _, err := v.ConstitutionPropose(ctx, []byte(next+"\n# c\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ConstitutionPropose(ctx, []byte(next+"\n# d\n")); err != nil {
		t.Fatal(err)
	}
	if err := srv.ApproveConstitution(ctx, 2, "9"); err != nil {
		t.Fatal(err)
	}
	if err := srv.ApproveConstitution(ctx, 3, "10"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale approve: %v", err)
	}
}
