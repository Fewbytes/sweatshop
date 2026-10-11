package linedir

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
)

// ok checks a git call: ok(t)(repo.Git(...)).
func ok(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func sourceRepo(t *testing.T) gitx.Repo {
	t.Helper()
	ctx := context.Background()
	src := gitx.Repo{Dir: t.TempDir()}
	ok(t)(src.Git(ctx, "init", "--quiet", "--initial-branch=master"))
	ok(t)(src.Git(ctx, "config", "user.name", "test"))
	ok(t)(src.Git(ctx, "config", "user.email", "test@example.com"))
	if err := os.WriteFile(filepath.Join(src.Dir, "README"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok(t)(src.Git(ctx, "add", "README"))
	ok(t)(src.Git(ctx, "commit", "--quiet", "-m", "seed"))
	return src
}

func initLine(t *testing.T) (Line, gitx.Repo) {
	t.Helper()
	src := sourceRepo(t)
	l, err := Init(context.Background(), InitOptions{Dir: filepath.Join(t.TempDir(), "line"), Source: src.Dir})
	if err != nil {
		t.Fatal(err)
	}
	return l, src
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestInitCreatesLayout(t *testing.T) {
	ctx := context.Background()
	l, src := initLine(t)
	if l.Port != 7468 || l.Engine != "docker" || l.Image != "sweatshop-station:dev" || len(l.Stations) != 2 {
		t.Fatalf("defaults: %+v", l)
	}
	for _, s := range l.Stations {
		if s.AgentID != s.Name {
			t.Errorf("agent id %q != name %q", s.AgentID, s.Name)
		}
		for _, f := range []string{s.KeyFile, s.TokenFile} {
			if m := mode(t, f); m != 0o600 {
				t.Errorf("%s mode = %o", f, m)
			}
		}
		if _, err := os.Stat(s.KeyFile + ".pub"); err != nil {
			t.Error(err)
		}
		if m := mode(t, s.StateDir); m != 0o700 {
			t.Errorf("state dir mode = %o", m)
		}
		tok, err := s.Token()
		if err != nil || len(tok) != 64 {
			t.Errorf("token = %q, %v", tok, err)
		}
	}
	c, _, err := constitution.Load(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	if c.Trust.Level != "attest" || len(c.Trust.Keys) != 2 {
		t.Fatalf("trust = %+v", c.Trust)
	}
	if c.Trust.Keys[0].Roles[0] != "maker" || c.Trust.Keys[1].Roles[0] != "andon" {
		t.Fatalf("roles = %+v", c.Trust.Keys)
	}
	// The check definition lives in the candidate; makers must not be able to
	// weaken it (ruling R13). Only gemba may change it, and M1 has no gemba
	// station, so in practice only the owner, directly on the host.
	for _, p := range []string{"justfile", "station/Dockerfile", "station/entrypoint.sh"} {
		roles, covered := c.AllowedRoles(p)
		if !covered || len(roles) != 1 || roles[0] != "gemba" {
			t.Errorf("AllowedRoles(%q) = %v %v, want [gemba]", p, roles, covered)
		}
	}
	if _, covered := c.AllowedRoles("line/maker/runner.go"); covered {
		t.Error("ordinary source is role-gated")
	}
	want, err := src.Git(ctx, "rev-parse", "refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	got, err := (gitx.Repo{Dir: l.Remote}).Git(ctx, "rev-parse", "refs/heads/master")
	if err != nil || got != want {
		t.Fatalf("remote master = %q, %v; want %q", got, err, want)
	}
	if _, err := os.Stat(filepath.Join(l.Remote, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRoundTrips(t *testing.T) {
	l, _ := initLine(t)
	got, err := Load(l.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != l.Port || got.Remote != l.Remote || got.Engine != l.Engine || got.Image != l.Image ||
		len(got.Stations) != 2 || got.Stations[0] != l.Stations[0] || got.Stations[1] != l.Stations[1] {
		t.Fatalf("got %+v, want %+v", got, l)
	}
}

func TestInitRefusesNonEmptyDir(t *testing.T) {
	src := sourceRepo(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), InitOptions{Dir: dir, Source: src.Dir}); err == nil {
		t.Fatal("init into non-empty dir succeeded")
	}
}

func TestLoadRejectsLooseModes(t *testing.T) {
	for _, pick := range []func(Station) string{
		func(s Station) string { return s.TokenFile },
		func(s Station) string { return s.KeyFile },
	} {
		l, _ := initLine(t)
		if err := os.Chmod(pick(l.Stations[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(l.Dir); err == nil {
			t.Fatal("Load accepted a world-readable secret")
		}
	}
}

func TestLoadRejectsMissingKey(t *testing.T) {
	l, _ := initLine(t)
	if err := os.Remove(l.Stations[1].KeyFile); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(l.Dir); err == nil {
		t.Fatal("Load accepted a missing key")
	}
}

func TestAddStation(t *testing.T) {
	l, _ := initLine(t)
	st, err := AddStation(l.Dir, StationSpec{Name: "viceroy-1", Role: "viceroy"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{st.KeyFile, st.TokenFile} {
		if m := mode(t, f); m != 0o600 {
			t.Errorf("%s mode = %o", f, m)
		}
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "stations", "viceroy-1", "station.toml")); err != nil {
		t.Error(err)
	}
	got, err := Load(l.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Stations) != 3 || got.Stations[2].Name != "viceroy-1" || got.Stations[2].Role != "viceroy" || got.Stations[2].AgentID != "viceroy-1" {
		t.Fatalf("stations = %+v", got.Stations)
	}
	c, _, err := constitution.Load(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := c.KeyRoles()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Trust.Keys) != 3 {
		t.Fatalf("keys = %+v", c.Trust.Keys)
	}
	k := c.Trust.Keys[2]
	if k.Name != "viceroy-1" || len(k.Roles) != 1 || k.Roles[0] != "viceroy" || len(roles) != 3 {
		t.Fatalf("key = %+v roles = %v", k, roles)
	}
	pub, err := os.ReadFile(st.KeyFile + ".pub")
	if err != nil || strings.TrimSpace(string(pub)) != k.PublicKey {
		t.Fatalf("trusted key %q does not match %q (%v)", k.PublicKey, pub, err)
	}
}

func TestAddStationCommitsKeyOntoConstitutionRef(t *testing.T) {
	ctx := context.Background()
	l, _ := initLine(t)
	repo := gitx.Repo{Dir: l.Remote}
	file, err := os.ReadFile(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	// The ref carries a setting the file lacks, as after an approved proposal.
	refData := append(file, []byte("\n[channel]\nkind = \"discord\"\nchannel = \"123456789\"\ntransport = \"gateway\"\n")...)
	if _, err := CommitConstitution(ctx, repo, refData, "", "seed", "line"); err != nil {
		t.Fatal(err)
	}
	if _, err := AddStation(l.Dir, StationSpec{Name: "viceroy-1", Role: "viceroy"}); err != nil {
		t.Fatal(err)
	}
	onRef, err := repo.ReadBlob(ctx, ConstitutionRef+":"+ConstitutionBlob)
	if err != nil {
		t.Fatal(err)
	}
	c, err := constitution.Parse(onRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Trust.Keys) != 3 || c.Trust.Keys[2].Name != "viceroy-1" || !strings.Contains(string(onRef), `transport = "gateway"`) {
		t.Fatalf("ref constitution = %s", onRef)
	}
	exported, err := os.ReadFile(l.Constitution)
	if err != nil || string(exported) != string(onRef) {
		t.Fatalf("constitution.toml is not an export of the ref: %v\n%s", err, exported)
	}
	log, err := repo.Git(ctx, "log", "--format=%s", ConstitutionRef)
	if err != nil || !strings.Contains(log, "station add viceroy-1") {
		t.Fatalf("log = %q, %v", log, err)
	}
}

func TestAddStationRefusals(t *testing.T) {
	l, _ := initLine(t)
	before, err := os.ReadFile(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []StationSpec{
		{"maker-1", "maker"}, // existing name
		{"x-1", "overlord"},  // unknown role
		{"../evil", "maker"}, // bad name
		{"", "maker"},
	} {
		if _, err := AddStation(l.Dir, spec); err == nil {
			t.Errorf("AddStation(%+v) succeeded", spec)
		}
	}
	after, _ := os.ReadFile(l.Constitution)
	if string(before) != string(after) {
		t.Error("constitution changed by refused adds")
	}
	if got, err := Load(l.Dir); err != nil || len(got.Stations) != 2 {
		t.Fatalf("stations = %+v, %v", got.Stations, err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "stations", "x-1")); err == nil {
		t.Error("refused station left files behind")
	}
}

func TestInitWritesGovernance(t *testing.T) {
	src := sourceRepo(t)
	l, err := Init(context.Background(), InitOptions{
		Dir: filepath.Join(t.TempDir(), "line"), Source: src.Dir,
		SovereignName: "alice", SovereignDiscord: "123", Channel: "456",
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := constitution.Load(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sovereign != (constitution.Sovereign{Name: "alice", Discord: "123"}) ||
		c.Channel != (constitution.Channel{Kind: "discord", Channel: "456"}) {
		t.Fatalf("governance = %+v %+v", c.Sovereign, c.Channel)
	}
	if _, err := Init(context.Background(), InitOptions{
		Dir: filepath.Join(t.TempDir(), "line"), Source: src.Dir, SovereignName: "alice",
	}); err == nil {
		t.Fatal("sovereign name without discord ID accepted")
	}
}
