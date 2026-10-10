package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/linedir"
	"github.com/Fewbytes/sweatshop/line/station"
)

func TestKeygenPrintsKeyIDAndPublicKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	var out bytes.Buffer
	if err := run([]string{"keygen", path}, &out); err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "key_id ") || !strings.Contains(out.String(), strings.TrimSpace(string(pub))) {
		t.Fatalf("output = %q", out.String())
	}
}

func TestGuardInstallHooks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := (gitx.Repo{Dir: filepath.Dir(dir)}).Git(context.Background(), "init", "--quiet", "--bare", dir); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"guard", "install-hooks", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run([]string{"frobnicate"}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown command accepted")
	}
	if err := run(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("empty command line accepted")
	}
}

// initLine creates a line whose single check fails, with output, when BROKEN
// exists, rewritten to run checks on the host so tests need no container
// engine. It returns the line dir and an agent clone of the line remote.
func initLine(t *testing.T) (string, gitx.Repo) {
	t.Helper()
	ctx := context.Background()
	src := gitx.Repo{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(src.Dir, "check.sh"), []byte("test ! -e BROKEN || { echo 'BROKEN is present'; exit 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=master"},
		{"config", "user.name", "t"}, {"config", "user.email", "t@example.com"},
		{"add", "check.sh"}, {"commit", "--quiet", "-m", "seed"},
	} {
		if _, err := src.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "line")
	l, err := linedir.Init(ctx, linedir.InitOptions{Dir: dir, Source: src.Dir, Check: "sh check.sh"})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := constitution.Load(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	c.Trust.Level, c.Runner = "none", constitution.Runner{Kind: "local"}
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Constitution, data, 0o644); err != nil {
		t.Fatal(err)
	}
	agent := gitx.Repo{Dir: filepath.Join(t.TempDir(), "agent")}
	for _, args := range [][]string{
		{"clone", "--quiet", l.Remote, agent.Dir},
		{"-C", agent.Dir, "config", "user.name", "t"}, {"-C", agent.Dir, "config", "user.email", "t@example.com"},
	} {
		if _, err := src.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	return dir, agent
}

// serveLine serves the line on a test listener and points the client
// commands at it with the named station's token.
func serveLine(t *testing.T, dir, stationName string) {
	t.Helper()
	l, err := linedir.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := station.NewFromLine(l)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	for _, s := range l.Stations {
		if s.Name == stationName {
			tok, err := s.Token()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
			t.Setenv("SWEATSHOP_LINE_TOKEN", tok)
		}
	}
}

func TestLineServeRefusesNonLoopback(t *testing.T) {
	dir, _ := initLine(t)
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.0.2.1:7468"} {
		err := run([]string{"line", "serve", "--dir", dir, "--addr", addr}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--unsafe-listen") {
			t.Errorf("serve on %s: %v", addr, err)
		}
	}
}

func TestLineClientNeedsEnv(t *testing.T) {
	t.Setenv("SWEATSHOP_LINE_URL", "")
	t.Setenv("SWEATSHOP_LINE_TOKEN", "x")
	if err := run([]string{"line", "status"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "SWEATSHOP_LINE_URL") {
		t.Fatalf("status without URL: %v", err)
	}
	t.Setenv("SWEATSHOP_LINE_URL", "http://127.0.0.1:1")
	t.Setenv("SWEATSHOP_LINE_TOKEN", "")
	if err := run([]string{"line", "status"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "SWEATSHOP_LINE_TOKEN") {
		t.Fatalf("status without token: %v", err)
	}
}

func TestLineMergeAndStatus(t *testing.T) {
	t.Setenv("SWEATSHOP_BEAD", "")
	t.Setenv("SWEATSHOP_ANDON_EVENT", "")
	ctx := context.Background()
	dir, agent := initLine(t)
	serveLine(t, dir, "maker-1")

	var out bytes.Buffer
	if err := run([]string{"line", "status"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "master ") {
		t.Fatalf("status output = %q", out.String())
	}

	if err := os.WriteFile(filepath.Join(agent.Dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.txt"}, {"commit", "--quiet", "-m", "a"}} {
		if _, err := agent.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	if err := run([]string{"line", "merge", "--repo", agent.Dir}, &out); err != nil {
		t.Fatalf("merge: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "merged") {
		t.Fatalf("merge output = %q", out.String())
	}

	if err := os.WriteFile(filepath.Join(agent.Dir, "BROKEN"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "BROKEN"}, {"commit", "--quiet", "-m", "break"}} {
		if _, err := agent.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	if err := run([]string{"line", "merge", "--repo", agent.Dir, "--rev", "HEAD"}, &out); err == nil {
		t.Fatalf("failing merge exited 0:\n%s", out.String())
	}
	for _, want := range []string{"rejected", "checks_failed", "BROKEN is present"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("merge output missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := run([]string{"line", "attest", "--repo", agent.Dir}, &out); err == nil {
		t.Fatalf("failing attest exited 0:\n%s", out.String())
	}
	if err := run([]string{"line", "evidence", "ev_0000000000000000"}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing evidence exited 0")
	}
}

func TestLineMergeRefusesMissingTrailer(t *testing.T) {
	ctx := context.Background()
	t.Setenv("SWEATSHOP_BEAD", "x")
	t.Setenv("SWEATSHOP_ANDON_EVENT", "")
	dir, agent := initLine(t)
	serveLine(t, dir, "maker-1")
	if err := os.WriteFile(filepath.Join(agent.Dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.txt"}, {"commit", "--quiet", "-m", "a"}} {
		if _, err := agent.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"line", "merge", "--repo", agent.Dir}, &out); err == nil {
		t.Fatalf("merge without trailer exited 0:\n%s", out.String())
	}
	if strings.Contains(out.String(), "merged") {
		t.Fatalf("commit merged despite missing trailer:\n%s", out.String())
	}
}

func TestLineInit(t *testing.T) {
	ctx := context.Background()
	src := gitx.Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=master"},
		{"config", "user.name", "t"}, {"config", "user.email", "t@example.com"},
		{"commit", "--quiet", "--allow-empty", "-m", "seed"},
	} {
		if _, err := src.Git(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "line")
	var out bytes.Buffer
	if err := run([]string{"line", "init", "--dir", dir, "--source", src.Dir}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{dir, "7468", "maker-1", "andon-1", "key_id"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q: %s", want, out.String())
		}
	}
	if err := run([]string{"line", "init", "--dir", dir}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing --source accepted")
	}
}

func TestLineStationAdd(t *testing.T) {
	dir, _ := initLine(t)
	var out bytes.Buffer
	if err := run([]string{"line", "station", "add", "--dir", dir, "--name", "viceroy-1", "--role", "viceroy"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "station viceroy-1 role viceroy key_id ") {
		t.Fatalf("output = %q", out.String())
	}
	l, err := linedir.Load(dir)
	if err != nil || len(l.Stations) != 3 {
		t.Fatalf("stations = %+v, %v", l.Stations, err)
	}
	for _, args := range [][]string{
		{"line", "station", "add", "--dir", dir, "--name", "viceroy-1", "--role", "viceroy"},
		{"line", "station", "add", "--dir", dir, "--name", "z-1", "--role", "nope"},
		{"line", "station", "add", "--dir", dir, "--name", "z-1"},
	} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Errorf("run(%v) succeeded", args)
		}
	}
}

func TestLineCards(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	cases := []struct {
		args  []string
		stdin string
		want  string
	}{
		{[]string{"list", "--status", "open", "--label", "line", "--parent", "x-1"}, "", "GET /v1/cards?label=line&parent=x-1&status=open "},
		{[]string{"show", "x-1.2"}, "", "GET /v1/cards/x-1.2 "},
		{[]string{"create", "--title", "T", "--type", "task", "--priority", "1", "--parent", "x-1"}, "desc\n", `POST /v1/cards {"title":"T","description":"desc","type":"task","priority":1,"parent":"x-1"}`},
		{[]string{"comment", "x-1"}, "hi\n", `POST /v1/cards/x-1/comments {"text":"hi"}`},
		{[]string{"offer", "x-1"}, "", "POST /v1/cards/x-1/offer "},
	}
	for _, tc := range cases {
		got = nil
		var out bytes.Buffer
		if err := lineCards(tc.args, strings.NewReader(tc.stdin), &out); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%v: request %q, want %q", tc.args, got, tc.want)
		}
		if strings.TrimSpace(out.String()) != `{"ok":true}` {
			t.Fatalf("%v: output %q", tc.args, out.String())
		}
	}
	for _, bad := range [][]string{{}, {"nope"}, {"show"}, {"offer", "a", "b"}} {
		if err := lineCards(bad, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if err := run([]string{"line", "cards", "show", "x-1"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
}
