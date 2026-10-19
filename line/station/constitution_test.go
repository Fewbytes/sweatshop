package station

import (
	"bytes"
	"context"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
	"github.com/Fewbytes/sweatshop/line/linedir"
)

const baseCons = "[trust]\nlevel = \"none\"\n\n[[checks.required]]\nname = \"ci\"\ncommand = \"true\"\n\n[runner]\nkind = \"local\"\n"

func consRig(t *testing.T) (*Server, Client, Client, *Channel, *fakeDiscordServer) {
	t.Helper()
	ctx := context.Background()
	repo := gitx.Repo{Dir: t.TempDir()}
	if _, err := gittest.Git(ctx, repo.Dir, "init", "--quiet", "--bare"); err != nil {
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

func TestLoadConstitutionWarnsWhenFileDiffersFromRef(t *testing.T) {
	ctx := context.Background()
	repo := gitx.Repo{Dir: t.TempDir()}
	if _, err := gittest.Git(ctx, repo.Dir, "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "c.toml")
	_ = os.WriteFile(file, []byte(baseCons), 0o644)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if _, _, err := LoadConstitution(ctx, repo, file); err != nil || logged.Len() != 0 {
		t.Fatalf("import: %v, logged %q", err, logged.String())
	}
	_ = os.WriteFile(file, []byte(baseCons+"\n"), 0o644)
	if _, _, err := LoadConstitution(ctx, repo, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), "differs from refs/line/constitution") {
		t.Fatalf("no drift warning: %q", logged.String())
	}
}

func TestLoadConstitutionImportsOnceThenReadsRef(t *testing.T) {
	ctx := context.Background()
	repo := gitx.Repo{Dir: t.TempDir()}
	if _, err := gittest.Git(ctx, repo.Dir, "init", "--quiet", "--bare"); err != nil {
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
	if ok, _ := repo.RefExists(ctx, linedir.ConstitutionRef); !ok {
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

func TestEveryRoleReadsConstitutionButOnlyViceroyProposes(t *testing.T) {
	repo := gitx.Repo{Dir: t.TempDir()}
	if _, err := gittest.Git(context.Background(), repo.Dir, "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "constitution.toml")
	if err := os.WriteFile(file, []byte(baseCons), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadConstitution(context.Background(), repo, file); err != nil {
		t.Fatal(err)
	}
	srv := &Server{}
	srv.Guard.Repo = repo
	for _, role := range constitution.Roles {
		srv.Stations = append(srv.Stations, &Station{Name: role + "-1", Role: role, Token: role + "-token"})
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	ctx := context.Background()
	for _, role := range constitution.Roles {
		c := Client{URL: ts.URL, Token: role + "-token"}
		if got, err := c.ConstitutionGet(ctx); err != nil || string(got) != baseCons {
			t.Errorf("role %s get: %q, %v", role, got, err)
		}
		if entries, err := c.ConstitutionLog(ctx); err != nil || len(entries) != 1 {
			t.Errorf("role %s log: %v, %v", role, entries, err)
		}
		if role == viceroyRole {
			continue
		}
		if _, err := c.ConstitutionPropose(ctx, []byte(baseCons+"\n# x\n")); err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("role %s proposal not refused with 403: %v", role, err)
		}
	}
}
