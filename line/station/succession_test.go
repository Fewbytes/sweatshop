package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fewbytes/sweatshop/line/internal/testwait"

	"github.com/pelletier/go-toml/v2"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
)

type fakeEngine struct {
	mu       sync.Mutex
	builds   []string // image tags
	buildDir []string
	starts   []ContainerSpec
	removed  []string
	running  map[string]bool
	buildErr error
}

func (e *fakeEngine) Build(_ context.Context, dir, dockerfile, tag string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.buildErr != nil {
		return e.buildErr
	}
	if _, err := os.Stat(dockerfile); err != nil {
		return err
	}
	e.builds = append(e.builds, tag)
	e.buildDir = append(e.buildDir, dir)
	return nil
}

func (e *fakeEngine) Start(_ context.Context, s ContainerSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts = append(e.starts, s)
	if e.running == nil {
		e.running = map[string]bool{}
	}
	e.running[s.Name] = true
	return nil
}

func (e *fakeEngine) Running(_ context.Context, name string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running[name], nil
}

func (e *fakeEngine) Remove(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removed = append(e.removed, name)
	delete(e.running, name)
	return nil
}

func (e *fakeEngine) exit(name string) {
	e.mu.Lock()
	e.running[name] = false
	e.mu.Unlock()
}

func (e *fakeEngine) removedNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.removed...)
}

type fakeChat struct {
	Discord // gateway methods are unused here and panic if called
	mu      sync.Mutex
	posted  []string
}

func (f *fakeChat) Me(context.Context) (string, error) { return "bot", nil }
func (f *fakeChat) Messages(context.Context, string, string) ([]DiscordMessage, error) {
	return nil, nil
}
func (f *fakeChat) Post(_ context.Context, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, text)
	return nil
}
func (f *fakeChat) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posted...)
}

type succRig struct {
	t      *testing.T
	ctx    context.Context
	line   linedir.Line
	srv    *Server
	sup    *Supervisor
	eng    *fakeEngine
	chat   *fakeChat
	ts     *httptest.Server
	agent  gitx.Repo
	remote gitx.Repo
	c1     string
}

func newSuccRig(t *testing.T, succession bool) *succRig {
	t.Helper()
	ctx := context.Background()
	src := gitx.Repo{Dir: t.TempDir()}
	ok(t)(gittest.Git(ctx, src.Dir, "init", "--quiet", "--initial-branch=master"))
	ok(t)(gittest.Git(ctx, src.Dir, "config", "user.name", "t"))
	ok(t)(gittest.Git(ctx, src.Dir, "config", "user.email", "t@example.com"))
	if err := os.WriteFile(filepath.Join(src.Dir, "check.sh"), []byte(checkScript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src.Dir, "station"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.Dir, "station", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok(t)(gittest.Git(ctx, src.Dir, "add", "-A"))
	ok(t)(gittest.Git(ctx, src.Dir, "commit", "--quiet", "-m", "seed"))
	l, err := linedir.Init(ctx, linedir.InitOptions{
		Dir: filepath.Join(t.TempDir(), "line"), Source: src.Dir, Check: "sh check.sh",
		Stations: []linedir.StationSpec{{Name: "maker-1", Role: "maker"}, {Name: "viceroy-1", Role: "viceroy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := constitution.Load(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	c.Trust.Level = "none"
	c.Runner = constitution.Runner{Kind: "local"}
	c.Viceroy.Succession = succession
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Constitution, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Dir, "model.cred"), []byte("model-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewFromLine(l)
	if err != nil {
		t.Fatal(err)
	}
	r := &succRig{t: t, ctx: ctx, line: l, srv: srv, eng: &fakeEngine{}, chat: &fakeChat{}, remote: gitx.Repo{Dir: l.Remote}}
	srv.Channel = &Channel{Dir: filepath.Join(l.Dir, "channel"), ChannelID: "555", Discord: r.chat}
	r.sup = NewSupervisor(srv, l, r.eng)
	r.sup.Log = nil
	r.sup.Poll = 10 * time.Millisecond
	r.sup.Timeout = time.Second
	r.ts = httptest.NewServer(srv)
	t.Cleanup(r.ts.Close)
	t.Cleanup(func() { r.sup.watchers.Wait() })
	r.agent = gitx.Repo{Dir: filepath.Join(t.TempDir(), "agent")}
	ok(t)(gittest.Git(ctx, src.Dir, "clone", "--quiet", l.Remote, r.agent.Dir))
	ok(t)(gittest.Git(ctx, r.agent.Dir, "config", "user.name", "t"))
	ok(t)(gittest.Git(ctx, r.agent.Dir, "config", "user.email", "t@example.com"))
	r.c1 = r.master()
	return r
}

func (r *succRig) master() string {
	r.t.Helper()
	sha, err := r.remote.Commit(r.ctx, "refs/heads/master")
	if err != nil {
		r.t.Fatal(err)
	}
	return sha
}

// land commits a file and moves the line's master there directly.
func (r *succRig) land(name, content string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.agent.Dir, name), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	ok(r.t)(gittest.Git(r.ctx, r.agent.Dir, "add", "-A"))
	ok(r.t)(gittest.Git(r.ctx, r.agent.Dir, "commit", "--quiet", "-m", "change "+name))
	ok(r.t)(gittest.Git(r.ctx, r.remote.Dir, "fetch", "--quiet", r.agent.Dir, "+HEAD:refs/heads/master"))
	return r.master()
}

func (r *succRig) client(station string, gen string) Client {
	r.t.Helper()
	for _, s := range r.line.Stations {
		if s.Name == station {
			tok, err := s.Token()
			if err != nil {
				r.t.Fatal(err)
			}
			return Client{URL: r.ts.URL, Token: tok, Gen: gen}
		}
	}
	r.t.Fatalf("no station %s", station)
	return Client{}
}

func (r *succRig) station(name string) *Station {
	for _, s := range r.srv.Stations {
		if s.Name == name {
			return s
		}
	}
	r.t.Fatalf("no station %s", name)
	return nil
}

func wantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("error = %v, want %q", err, sub)
	}
}

func (r *succRig) bootstrap() {
	r.t.Helper()
	if _, err := r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), r.c1); err != nil {
		r.t.Fatal(err)
	}
}

func TestBootstrap(t *testing.T) {
	r := newSuccRig(t, true)
	g, err := r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), r.c1)
	if err != nil || g.Gen != 1 {
		t.Fatalf("bootstrap %+v %v", g, err)
	}
	if len(r.eng.builds) != 1 || r.eng.builds[0] != "sweatshop-viceroy:"+r.c1 {
		t.Fatalf("builds %v", r.eng.builds)
	}
	s := r.eng.starts[0]
	if s.Name != "sweatshop-viceroy-1" || s.Env["SWEATSHOP_MODE"] != "viceroy" || s.Env["SWEATSHOP_VICEROY_GEN"] != "1" ||
		s.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "model-token" || s.Env["SWEATSHOP_LINE_TOKEN"] == "" ||
		s.Env["SWEATSHOP_PREDECESSOR"] != "" || s.Memory != constitution.DefaultMemory || s.Pids != constitution.DefaultPids {
		t.Fatalf("spec %+v", s)
	}
	if len(s.Mounts) != 1 || s.Mounts[0] != (Mount{Host: r.line.Remote, Container: "/line/remote.git", ReadOnly: true}) {
		t.Fatalf("mounts %+v", s.Mounts)
	}
	cur, have, err := r.sup.current()
	if err != nil || !have || cur.Gen != 1 || cur.Commit != r.c1 {
		t.Fatalf("current %+v %v %v", cur, have, err)
	}
	if _, err := os.Stat(r.eng.buildDir[0]); err == nil {
		t.Fatal("checkout left behind")
	}
	// A second bootstrap, or one at a non-master commit, is refused.
	_, err = r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), r.c1)
	wantErr(t, err, "already has a viceroy generation")
}

// A project line has no station/Dockerfile in its master: with viceroy_image
// set, the generation runs that image and nothing is built.
func TestBootstrapWithViceroyImageBuildsNothing(t *testing.T) {
	r := newSuccRig(t, true)
	r.sup.Line.ViceroyImage = "localhost/sweatshop-station:dev"
	g, err := r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), r.c1)
	if err != nil || g.Gen != 1 {
		t.Fatalf("bootstrap %+v %v", g, err)
	}
	if len(r.eng.builds) != 0 {
		t.Fatalf("builds %v, want none", r.eng.builds)
	}
	if got := r.eng.starts[0].Image; got != "localhost/sweatshop-station:dev" {
		t.Fatalf("image %q", got)
	}
}

func TestBootstrapReplacesCrashedGenerationOne(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	r.eng.running[ContainerName(1)] = false // generation 1 crashed
	g, err := r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), r.c1)
	if err != nil || g.Gen != 1 {
		t.Fatalf("bootstrap over crashed gen 1: %+v %v", g, err)
	}
	if len(r.eng.starts) != 2 || !r.eng.running[ContainerName(1)] {
		t.Fatalf("starts=%d running=%v", len(r.eng.starts), r.eng.running)
	}
}

func TestBootstrapRefusals(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	r := newSuccRig(t, true)
	_, err := r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), strings.Repeat("a", 40))
	wantErr(t, err, "not the line's master")
	ok(t)(gittest.Git(r.ctx, r.remote.Dir, "update-ref", guard.StopRef, r.c1))
	_, err = r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), r.c1)
	wantErr(t, err, "stopped")
	ok(t)(gittest.Git(r.ctx, r.remote.Dir, "update-ref", "-d", guard.StopRef))
	if err := os.Remove(filepath.Join(r.line.Dir, "model.cred")); err != nil {
		t.Fatal(err)
	}
	_, err = r.sup.Bootstrap(r.ctx, r.station("viceroy-1"), r.c1)
	wantErr(t, err, "model credential")
	if len(r.eng.starts) != 0 {
		t.Fatal("started a container")
	}
}

func TestSucceedAndCheckin(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	gen1 := r.client("viceroy-1", "1")
	gen2 := r.client("viceroy-1", "2")

	out, err := gen1.ViceroySucceed(r.ctx, c2)
	if err != nil {
		t.Fatal(err)
	}
	var g Generation
	if err := json.Unmarshal(out, &g); err != nil || g.Gen != 2 || g.Commit != c2 || g.Predecessor != r.c1 {
		t.Fatalf("response %s %v", out, err)
	}
	if len(r.eng.builds) != 2 || r.eng.builds[1] != "sweatshop-viceroy:"+c2 {
		t.Fatalf("builds %v", r.eng.builds)
	}
	s := r.eng.starts[1]
	if s.Name != "sweatshop-viceroy-2" || s.Env["SWEATSHOP_VICEROY_GEN"] != "2" || s.Env["SWEATSHOP_PREDECESSOR"] != r.c1 {
		t.Fatalf("spec %+v", s)
	}

	// Until check-in: gen 1 still works; gen 2 gets exactly one post.
	if err := gen1.ChannelPost(r.ctx, "from 1", ""); err != nil {
		t.Fatal(err)
	}
	if err := gen2.ChannelPost(r.ctx, "hello from 2", ""); err != nil {
		t.Fatal(err)
	}
	wantErr(t, gen2.ChannelPost(r.ctx, "again", ""), "409")
	_, err = gen2.CardComment(r.ctx, "x-1", "no")
	wantErr(t, err, "409")
	// A second succession while one is pending is refused.
	_, err = gen1.ViceroySucceed(r.ctx, c2)
	wantErr(t, err, "409")
	// The old generation cannot check in the new one.
	_, err = gen1.ViceroyCheckin(r.ctx)
	wantErr(t, err, "409")

	if _, err := gen2.ViceroyCheckin(r.ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.eng.removedNames(); got[len(got)-1] != "sweatshop-viceroy-1" {
		t.Fatalf("removed %v", got)
	}
	cur, _, _ := r.sup.current()
	if cur.Gen != 2 || cur.Commit != c2 {
		t.Fatalf("current %+v", cur)
	}
	gens, err := r.sup.Generations()
	if err != nil || len(gens) != 2 || gens[0].Gen != 1 || gens[1].Gen != 2 {
		t.Fatalf("generations %+v %v", gens, err)
	}
	// Now gen 1 is refused and gen 2 posts freely.
	wantErr(t, gen1.ChannelPost(r.ctx, "late", ""), "409")
	if err := gen2.ChannelPost(r.ctx, "one", ""); err != nil {
		t.Fatal(err)
	}
	if err := gen2.ChannelPost(r.ctx, "two", ""); err != nil {
		t.Fatal(err)
	}
	// With nothing pending, the current generation may check in again (a
	// restart after Resume) and nothing changes; any other generation may not.
	out, err = gen2.ViceroyCheckin(r.ctx)
	var again Generation
	if err != nil || json.Unmarshal(out, &again) != nil || again.Gen != 2 || again.Commit != c2 {
		t.Fatalf("repeat check-in %s %v", out, err)
	}
	_, err = gen1.ViceroyCheckin(r.ctx)
	wantErr(t, err, "409")
	_, err = r.client("viceroy-1", "3").ViceroyCheckin(r.ctx)
	wantErr(t, err, "409")
}

func TestSucceedRefusals(t *testing.T) {
	r := newSuccRig(t, true)
	v := r.client("viceroy-1", "")
	// No generation 1 yet.
	_, err := v.ViceroySucceed(r.ctx, r.c1)
	wantErr(t, err, "409")
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	v.Gen = "1"

	_, err = v.ViceroySucceed(r.ctx, "nothex")
	wantErr(t, err, "400")
	_, err = v.ViceroySucceed(r.ctx, strings.Repeat("b", 40))
	wantErr(t, err, "not the line's master")
	_, err = v.ViceroySucceed(r.ctx, r.c1) // the current generation's commit
	wantErr(t, err, "409")
	ok(t)(gittest.Git(r.ctx, r.remote.Dir, "update-ref", guard.StopRef, c2))
	_, err = v.ViceroySucceed(r.ctx, c2)
	wantErr(t, err, "stopped")
	ok(t)(gittest.Git(r.ctx, r.remote.Dir, "update-ref", "-d", guard.StopRef))
	// A stale generation may not ask.
	stale := v
	stale.Gen = "7"
	_, err = stale.ViceroySucceed(r.ctx, c2)
	wantErr(t, err, "409")
	if len(r.eng.starts) != 1 {
		t.Fatalf("starts %d", len(r.eng.starts))
	}
}

func TestSucceedSameCommitAsCurrent(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	_, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, r.c1)
	wantErr(t, err, "already generation 1")
}

func TestSucceedNeedsConstitution(t *testing.T) {
	r := newSuccRig(t, false)
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	_, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2)
	wantErr(t, err, "403")
}

func TestSucceedChecksMustPass(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	bad := r.land("BROKEN", "x")
	_, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, bad)
	wantErr(t, err, "checks failed")
	if len(r.eng.builds) != 1 {
		t.Fatalf("built despite failing checks: %v", r.eng.builds)
	}
}

func TestSucceedBuildFailure(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	r.eng.buildErr = errors.New("boom")
	_, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2)
	wantErr(t, err, "502")
	cur, _, _ := r.sup.current()
	if cur.Gen != 1 || len(r.eng.starts) != 1 {
		t.Fatalf("current %+v starts %d", cur, len(r.eng.starts))
	}
	// Not stuck pending: a retry is accepted.
	r.eng.buildErr = nil
	if _, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	testwait.Until(t, 5*time.Second, what, f)
}

func TestRollbackOnTimeout(t *testing.T) {
	r := newSuccRig(t, true)
	r.sup.Timeout = 50 * time.Millisecond
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	if _, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "rollback", func() bool { return len(r.chat.all()) > 0 })
	if got := r.eng.removedNames(); got[len(got)-1] != "sweatshop-viceroy-2" {
		t.Fatalf("removed %v", got)
	}
	if msg := r.chat.all()[0]; !strings.Contains(msg, "failed") || !strings.Contains(msg, "no check-in") {
		t.Fatalf("post %q", msg)
	}
	cur, _, _ := r.sup.current()
	if cur.Gen != 1 {
		t.Fatalf("current %+v", cur)
	}
	// Late check-in is refused; gen 1 still posts; a new attempt may start.
	_, err := r.client("viceroy-1", "2").ViceroyCheckin(r.ctx)
	wantErr(t, err, "409")
	if err := r.client("viceroy-1", "1").ChannelPost(r.ctx, "still here", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackOnEarlyExit(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	if _, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2); err != nil {
		t.Fatal(err)
	}
	before := len(r.eng.removedNames())
	r.eng.exit("sweatshop-viceroy-2")
	waitFor(t, "rollback", func() bool { return len(r.chat.all()) > 0 })
	if msg := r.chat.all()[0]; !strings.Contains(msg, "exited") {
		t.Fatalf("post %q", msg)
	}
	for _, n := range r.eng.removedNames()[before:] {
		if n == "sweatshop-viceroy-1" {
			t.Fatal("old generation was stopped")
		}
	}
}

func TestResume(t *testing.T) {
	r := newSuccRig(t, true)
	if err := r.sup.Resume(r.ctx); err != nil || len(r.eng.starts) != 0 {
		t.Fatalf("no generation: %v starts=%d", err, len(r.eng.starts))
	}
	r.bootstrap()
	if err := r.sup.Resume(r.ctx); err != nil || len(r.eng.starts) != 1 {
		t.Fatalf("running: %v starts=%d", err, len(r.eng.starts))
	}
	r.eng.exit("sweatshop-viceroy-1")
	if err := r.sup.Resume(r.ctx); err != nil || len(r.eng.starts) != 2 {
		t.Fatalf("stopped: %v starts=%d", err, len(r.eng.starts))
	}
	if s := r.eng.starts[1]; s.Name != "sweatshop-viceroy-1" || s.Env["SWEATSHOP_VICEROY_GEN"] != "1" {
		t.Fatalf("spec %+v", s)
	}
}

func TestViceroyRoutesRoleAndMethod(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	m := r.client("maker-1", "")
	_, err := m.ViceroySucceed(r.ctx, r.c1)
	wantErr(t, err, "403")
	_, err = m.ViceroyCheckin(r.ctx)
	wantErr(t, err, "403")
	req, _ := http.NewRequest(http.MethodGet, r.ts.URL+"/v1/viceroy/succeed", nil)
	req.Header.Set("Authorization", "Bearer "+r.client("viceroy-1", "").Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	_, err = r.client("viceroy-1", "").do(r.ctx, http.MethodPost, "/v1/viceroy/nope", nil, nil, nil)
	wantErr(t, err, "404")
	// Maker writes are not generation-gated by the viceroy guard.
	srv := &Server{Stations: []*Station{{Name: "v", Role: "viceroy", Token: "vt"}}}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	_, err = Client{URL: ts.URL, Token: "vt"}.ViceroyCheckin(r.ctx)
	wantErr(t, err, "503")
}

func TestCLIEngineStartMounts(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "fakedocker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + out + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := CLIEngine{Bin: bin}
	spec := ContainerSpec{Image: "img", Name: "n", Network: "line-a", Mounts: []Mount{{Host: "/srv/remote.git", Container: "/line/remote.git", ReadOnly: true}}}
	if err := e.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "-v\n/srv/remote.git:/line/remote.git:ro\n") {
		t.Fatalf("argv %q", b)
	}
	if !strings.Contains(string(b), "--network\nline-a\n") {
		t.Fatalf("viceroy container not on the line network: argv %q", b)
	}
	for _, m := range []Mount{{Host: "rel", Container: "/c"}, {Host: "/a:b", Container: "/c"}, {Host: "/a", Container: "c"}} {
		spec.Mounts = []Mount{m}
		if err := e.Start(context.Background(), spec); err == nil {
			t.Fatalf("mount %+v accepted", m)
		}
	}
}

func TestSucceedFromSupersededCommitLaunchesCurrentMaster(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	c3 := r.land("b.txt", "b")
	out, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2)
	if err != nil {
		t.Fatal(err)
	}
	var g Generation
	if err := json.Unmarshal(out, &g); err != nil || g.Gen != 2 || g.Commit != c3 {
		t.Fatalf("want generation 2 at %s, got %s %v", c3, out, err)
	}
}

func TestSucceedWaitsForCheckSlot(t *testing.T) {
	r := newSuccRig(t, true)
	r.bootstrap()
	c2 := r.land("a.txt", "a")
	if !r.srv.acquire() {
		t.Fatal("slot already taken")
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.client("viceroy-1", "1").ViceroySucceed(r.ctx, c2)
		done <- err
	}()
	r.srv.busy.Store(false)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAcquireQueuedGivesUpAfterLimit(t *testing.T) {
	var s Server
	if !s.acquire() {
		t.Fatal("slot already taken")
	}
	if err := s.acquireQueued(context.Background(), 20*time.Millisecond); !errors.Is(err, errBusy) {
		t.Fatalf("want errBusy, got %v", err)
	}
}

func TestBootstrapUsesLineURLElseHostGatewayDefault(t *testing.T) {
	r := newSuccRig(t, true)
	r.sup.Line.URL = "http://sweatshop-a-serve:7469"
	r.bootstrap()
	if got := r.eng.starts[0].Env["SWEATSHOP_LINE_URL"]; got != "http://sweatshop-a-serve:7469" {
		t.Fatalf("SWEATSHOP_LINE_URL = %q, want the line's url", got)
	}
	r = newSuccRig(t, true)
	r.sup.Line.URL = ""
	r.bootstrap()
	want := fmt.Sprintf("http://host.containers.internal:%d", r.sup.Line.Port)
	if got := r.eng.starts[0].Env["SWEATSHOP_LINE_URL"]; got != want {
		t.Fatalf("SWEATSHOP_LINE_URL = %q, want %q", got, want)
	}
}
