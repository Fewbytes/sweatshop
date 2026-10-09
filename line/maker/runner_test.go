package maker

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/linedir"
)

func ok(t *testing.T) func(string, error) {
	return func(_ string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// remote returns a bare repository whose master holds one seed commit, and
// a work clone that can push to it.
func remote(t *testing.T) (bare, work gitx.Repo) {
	t.Helper()
	ctx := context.Background()
	bare = gitx.Repo{Dir: filepath.Join(t.TempDir(), "remote.git")}
	ok(t)(gitx.Repo{Dir: t.TempDir()}.Git(ctx, "init", "--quiet", "--bare", "--initial-branch=master", bare.Dir))
	work = gitx.Repo{Dir: t.TempDir()}
	ok(t)(work.Git(ctx, "init", "--quiet", "--initial-branch=master"))
	ok(t)(work.Git(ctx, "config", "user.name", "test"))
	ok(t)(work.Git(ctx, "config", "user.email", "test@example.com"))
	ok(t)(work.Git(ctx, "remote", "add", "origin", bare.Dir))
	commit(t, work, "seed")
	return bare, work
}

// commit makes an empty commit with message msg and pushes it to master.
func commit(t *testing.T, work gitx.Repo, msg string) {
	t.Helper()
	ctx := context.Background()
	ok(t)(work.Git(ctx, "commit", "--quiet", "--allow-empty", "-m", msg))
	ok(t)(work.Git(ctx, "push", "--quiet", "origin", "master"))
}

func TestPickOrdering(t *testing.T) {
	beads := []Bead{
		{ID: "b-3", Priority: 2},
		{ID: "b-2", Priority: 1},
		{ID: "b-1", Priority: 1},
		{ID: "b-9", Priority: 3, Labels: []string{"line", "andon"}},
		{ID: "b-0", Priority: 4},
	}
	got, found := Pick(beads)
	if !found || got.ID != "b-9" {
		t.Fatalf("Pick = %v %v, want andon card b-9 first", got.ID, found)
	}
	got, _ = Pick(beads[:3])
	if got.ID != "b-1" {
		t.Fatalf("Pick = %v, want b-1 (lowest priority number, then ID)", got.ID)
	}
	if _, found := Pick(nil); found {
		t.Fatal("Pick(nil) found a bead")
	}
}

func TestLanded(t *testing.T) {
	ctx := context.Background()
	bare, work := remote(t)
	if landed, err := Landed(ctx, bare, "b-7"); err != nil || landed {
		t.Fatalf("Landed before = %v %v, want false", landed, err)
	}
	commit(t, work, "unrelated\n\nBead: b-70")
	commit(t, work, "do the thing\n\nBead: b-7\nAndon-Event: e1")
	commit(t, work, "later")
	if landed, err := Landed(ctx, bare, "b-7"); err != nil || !landed {
		t.Fatalf("Landed after = %v %v, want true", landed, err)
	}
	if landed, err := Landed(ctx, bare, "b-8"); err != nil || landed {
		t.Fatalf("Landed(b-8) = %v %v, want false", landed, err)
	}
}

type fakeBeads struct {
	ready  []Bead
	calls  []string
	claims map[string]string
}

func (f *fakeBeads) Ready(_ context.Context, label string) ([]Bead, error) {
	f.calls = append(f.calls, "ready "+label)
	return f.ready, nil
}

func (f *fakeBeads) Claim(_ context.Context, id, agent string) error {
	f.calls = append(f.calls, "claim "+id+" "+agent)
	return nil
}

func (f *fakeBeads) Close(_ context.Context, id, reason string) error {
	f.calls = append(f.calls, "close "+id+" "+reason)
	return nil
}

func (f *fakeBeads) Release(_ context.Context, id, note string) error {
	f.calls = append(f.calls, "release "+id+" "+note)
	return nil
}

type fakeEngine struct {
	specs []ContainerSpec
	run   func(spec ContainerSpec) (int, error)
}

func (f *fakeEngine) Run(_ context.Context, spec ContainerSpec) (int, error) {
	f.specs = append(f.specs, spec)
	if f.run == nil {
		return 0, nil
	}
	return f.run(spec)
}

func newRunner(t *testing.T, bare gitx.Repo, b Beads, e Engine) *Runner {
	t.Helper()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("tok-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Runner{
		Line:    linedir.Line{Dir: dir, Port: 7468, Remote: bare.Dir, Image: "sweatshop-station:test"},
		Station: linedir.Station{Name: "maker-1", Role: "maker", AgentID: "maker-1", TokenFile: tokenFile},
		Beads:   b, Engine: e,
		Log: log.New(&bytes.Buffer{}, "", 0),
	}
}

func TestOnceClosesLandedBead(t *testing.T) {
	bare, work := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-2", Title: "two", Priority: 2}, {ID: "b-1", Title: "one", Priority: 1}}}
	fe := &fakeEngine{run: func(spec ContainerSpec) (int, error) {
		commit(t, work, "work\n\nBead: "+spec.Env["SWEATSHOP_BEAD"])
		return 0, nil
	}}
	r := newRunner(t, bare, fb, fe)
	worked, err := r.Once(context.Background())
	if err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	want := []string{"ready line", "claim b-1 maker-1", "close b-1 landed via line"}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Fatalf("calls = %q, want %q", fb.calls, want)
	}
}

func TestOnceReleasesUnlandedBead(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Title: "one", Priority: 1}}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return 3, nil }}
	r := newRunner(t, bare, fb, fe)
	worked, err := r.Once(context.Background())
	if err != nil || !worked {
		t.Fatalf("Once = %v %v", worked, err)
	}
	want := []string{"ready line", "claim b-1 maker-1", "release b-1 maker run ended without landing (exit 3)"}
	if !reflect.DeepEqual(fb.calls, want) {
		t.Fatalf("calls = %q, want %q", fb.calls, want)
	}
}

func TestOnceReleasesWhenEngineFails(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Priority: 1}}}
	fe := &fakeEngine{run: func(ContainerSpec) (int, error) { return -1, errors.New("no engine") }}
	r := newRunner(t, bare, fb, fe)
	if _, err := r.Once(context.Background()); err == nil {
		t.Fatal("Once hid the engine failure")
	}
	if last := fb.calls[len(fb.calls)-1]; !strings.HasPrefix(last, "release b-1 ") {
		t.Fatalf("last call = %q, want a release", last)
	}
}

func TestOnceIdle(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{}
	fe := &fakeEngine{}
	worked, err := newRunner(t, bare, fb, fe).Once(context.Background())
	if err != nil || worked || len(fe.specs) != 0 {
		t.Fatalf("Once = %v %v with %d runs, want idle", worked, err, len(fe.specs))
	}
}

func TestOnceContainerSpec(t *testing.T) {
	bare, _ := remote(t)
	fb := &fakeBeads{ready: []Bead{{ID: "b-1", Title: "one", Priority: 1}}}
	fe := &fakeEngine{}
	r := newRunner(t, bare, fb, fe)
	r.Creds = HostCreds(func(string) (string, bool) { return "", false })
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec := fe.specs[0]
	if spec.Image != "sweatshop-station:test" || len(spec.Args) != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Env["SWEATSHOP_LINE_TOKEN"] != "tok-123" || spec.Env["SWEATSHOP_LINE_URL"] != "http://host.containers.internal:7468" {
		t.Fatalf("env = %v", spec.Env)
	}
	if spec.Env["SWEATSHOP_BEAD"] != "b-1" || spec.Env["SWEATSHOP_AGENT_ID"] != "maker-1" || !strings.Contains(spec.Env["SWEATSHOP_PROMPT"], "Bead: b-1") {
		t.Fatalf("env = %v", spec.Env)
	}
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if _, has := spec.Env[k]; has {
			t.Fatalf("env carries %s although the host has none", k)
		}
	}
	want := []Mount{{Host: bare.Dir, Container: "/line/remote.git", ReadOnly: true}}
	if !reflect.DeepEqual(spec.Mounts, want) {
		t.Fatalf("mounts = %+v, want %+v", spec.Mounts, want)
	}

	r.URL = "http://example.test:9"
	r.Creds = HostCreds(func(k string) (string, bool) { return "secret-" + k, k == "ANTHROPIC_API_KEY" })
	fb.ready = []Bead{{ID: "b-2"}}
	if _, err := r.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec = fe.specs[1]
	if spec.Env["ANTHROPIC_API_KEY"] != "secret-ANTHROPIC_API_KEY" || spec.Env["SWEATSHOP_LINE_URL"] != "http://example.test:9" {
		t.Fatalf("env = %v", spec.Env)
	}
	if _, has := spec.Env["CLAUDE_CODE_OAUTH_TOKEN"]; has {
		t.Fatal("unset credential passed through")
	}
}

func TestPrompt(t *testing.T) {
	plain := Prompt(Bead{ID: "b-1", Title: "Add a flag", Description: "Make it so."}, "")
	for _, want := range []string{"b-1", "Add a flag", "Make it so.", "Bead: b-1", "/work", "just ci", "sweatshop line merge", "base_moved", "checks_failed", "RESULT: merged", "RESULT: gave-up"} {
		if !strings.Contains(plain, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(plain, "Andon-Event") {
		t.Error("plain card prompt mentions Andon-Event")
	}

	card := Bead{ID: "b-2", Title: "andon", Labels: []string{"line", "andon"}, Description: "pulled\n\nevent: 0123abcd\ncommit: ff"}
	ev := AndonEvent(card)
	if ev != "0123abcd" {
		t.Fatalf("AndonEvent = %q", ev)
	}
	if p := Prompt(card, ev); !strings.Contains(p, "Andon-Event: 0123abcd") || !strings.Contains(p, "Bead: b-2") {
		t.Fatalf("andon prompt lacks trailers:\n%s", p)
	}
	if AndonEvent(Bead{Description: "event: x"}) != "" {
		t.Fatal("AndonEvent parsed a card without the andon label")
	}
}

// fakeBin installs an executable script named name on PATH that appends its
// arguments (one per line, then a blank line) to the returned log file.
func fakeBin(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + logFile + "; done\necho >> " + logFile + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func calls(t *testing.T, logFile string) []string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range strings.Split(strings.TrimRight(string(data), "\n"), "\n\n") {
		out = append(out, strings.ReplaceAll(c, "\n", " "))
	}
	return out
}

func TestBeadsCLI(t *testing.T) {
	logFile := fakeBin(t, "bd", `case "$1" in ready) echo '[{"id":"b-1","title":"t","description":"d","priority":2,"labels":["line"]}]';; esac
`)
	ctx := context.Background()
	b := BeadsCLI{Dir: t.TempDir(), Actor: "maker-1"}
	got, err := b.Ready(ctx, "line")
	if err != nil {
		t.Fatal(err)
	}
	if want := []Bead{{ID: "b-1", Title: "t", Description: "d", Priority: 2, Labels: []string{"line"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ready = %+v, want %+v", got, want)
	}
	if err := b.Claim(ctx, "b-1", "maker-1"); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(ctx, "b-1", "landed via line"); err != nil {
		t.Fatal(err)
	}
	if err := b.Release(ctx, "b-1", "no luck"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ready --label line --limit 0 --json",
		"update b-1 --claim --actor maker-1",
		"close b-1 --reason landed via line --actor maker-1",
		"unclaim b-1 --reason no luck --actor maker-1",
		"comments add b-1 no luck --actor maker-1",
	}
	if got := calls(t, logFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("bd calls =\n%q\nwant\n%q", got, want)
	}
	if err := (BeadsCLI{Dir: t.TempDir()}).Claim(ctx, "-rf", "maker-1"); err == nil {
		t.Fatal("Claim accepted an option-like ID")
	}
}

func TestDockerEngine(t *testing.T) {
	logFile := fakeBin(t, "docker", `printf '%s' "$SWEATSHOP_LINE_TOKEN" > "$(dirname "$0")/token.seen"
exit 4
`)
	e := DockerEngine{Bin: "docker"}
	code, err := e.Run(context.Background(), ContainerSpec{
		Image: "img:1", Name: "maker-1-b-1",
		Env:    map[string]string{"SWEATSHOP_LINE_TOKEN": "tok", "A": "1"},
		Mounts: []Mount{{Host: "/h/remote.git", Container: "/line/remote.git", ReadOnly: true}, {Host: "/h/x", Container: "/x"}},
	})
	if err != nil || code != 4 {
		t.Fatalf("Run = %d %v, want exit 4", code, err)
	}
	want := []string{"run --rm --name maker-1-b-1 -e A -e SWEATSHOP_LINE_TOKEN -v /h/remote.git:/line/remote.git:ro -v /h/x:/x img:1"}
	if got := calls(t, logFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("docker calls = %q, want %q", got, want)
	}
	// Values travel in the engine's environment, never on its command line,
	// so other local users cannot read the token from the process table.
	seen, err := os.ReadFile(filepath.Join(filepath.Dir(logFile), "token.seen"))
	if err != nil || string(seen) != "tok" {
		t.Fatalf("token seen by engine = %q %v", seen, err)
	}
	if _, err := e.Run(context.Background(), ContainerSpec{Image: "img", Mounts: []Mount{{Host: "/a:b", Container: "/c"}}}); err == nil {
		t.Fatal("Run accepted a mount path containing ':'")
	}
}
