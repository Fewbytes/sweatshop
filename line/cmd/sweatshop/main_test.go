package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func TestLineClientTrimsTokenWhitespace(t *testing.T) {
	t.Setenv("SWEATSHOP_LINE_URL", "http://127.0.0.1:1")
	t.Setenv("SWEATSHOP_LINE_TOKEN", " tok\n")
	c, err := lineClient()
	if err != nil || c.Token != "tok" {
		t.Fatalf("lineClient token = %q, err = %v; want %q", c.Token, err, "tok")
	}
	t.Setenv("SWEATSHOP_LINE_TOKEN", "\n")
	if _, err := lineClient(); err == nil || !strings.Contains(err.Error(), "SWEATSHOP_LINE_TOKEN is empty") {
		t.Fatalf("whitespace-only token: %v", err)
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

func TestChunkText(t *testing.T) {
	long := strings.Repeat("x", 25)
	got := chunkText("aaaa\nbbbb\ncccc\n"+long+"\n", 10)
	want := []string{"aaaa\nbbbb", "cccc", long[:10], long[10:20], long[20:]}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(chunkText("  \n\n", 10)) != 0 {
		t.Fatal("blank text produced chunks")
	}
}

func TestLineChannel(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	var out bytes.Buffer
	if err := lineChannel([]string{"inbox", "--after", "7", "--wait", "30s"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if got[0] != "GET /v1/channel/inbox?after=7&wait=30s " || strings.TrimSpace(out.String()) != `{"messages":[]}` {
		t.Fatalf("%q %q", got, out.String())
	}
	got = nil
	text := strings.Repeat("l", 1000) + "\n" + strings.Repeat("m", 1000) + "\n"
	if err := lineChannel([]string{"post"}, strings.NewReader(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "POST /v1/channel/post ") {
		t.Fatalf("posts %q", got)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"inbox", "x"}} {
		if err := lineChannel(bad, strings.NewReader(""), &out); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if err := lineChannel([]string{"post"}, strings.NewReader("\n"), &out); err == nil {
		t.Fatal("empty post accepted")
	}
	if err := run([]string{"line", "channel", "inbox"}, &out); err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
}

func TestLineTestament(t *testing.T) {
	var stored []byte
	have := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			stored, _ = io.ReadAll(r.Body)
			have = true
			_, _ = w.Write([]byte(`{}`))
		case http.MethodGet:
			if !have {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = w.Write(stored)
		}
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	var out bytes.Buffer
	if err := lineTestament([]string{"get"}, strings.NewReader(""), &out); err != nil || out.Len() != 0 {
		t.Fatalf("empty get: %q %v", out.String(), err)
	}
	if err := lineTestament([]string{"put"}, strings.NewReader("my notes\n"), &out); err != nil {
		t.Fatal(err)
	}
	if err := lineTestament([]string{"get"}, strings.NewReader(""), &out); err != nil || out.String() != "my notes\n" {
		t.Fatalf("get: %q %v", out.String(), err)
	}
	if err := lineTestament([]string{"put"}, strings.NewReader(strings.Repeat("x", 64<<10+1)), &out); err == nil {
		t.Fatal("oversized put accepted")
	}
	for _, bad := range [][]string{{}, {"nope"}, {"get", "x"}} {
		if err := lineTestament(bad, strings.NewReader(""), &out); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if err := run([]string{"line", "testament", "get"}, &out); err != nil {
		t.Fatalf("run dispatch: %v", err)
	}
}

type stubAgent struct {
	prompts []string
	// keepRunning skips the self-SIGTERM, for tests where viceroyRun returns
	// on its own: the signal could land after its handler is unregistered
	// and kill the test binary.
	keepRunning bool
}

func (a *stubAgent) Run(_ context.Context, p string) (string, error) {
	a.prompts = append(a.prompts, p)
	// Stop the loop the way the container's supervisor would.
	if !a.keepRunning {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}
	return "done", nil
}

func TestViceroyRun(t *testing.T) {
	var testament string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			_, _ = w.Write([]byte(`{"master":"abc123"}`))
		case "/v1/channel/inbox":
			if r.URL.Query().Get("after") == "" {
				_, _ = w.Write([]byte(`{"messages":[{"id":"42","author":"k","author_id":"1","principal":"king","text":"hi viceroy","ts":"t"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"messages":[]}`))
			}
		case "/v1/testament":
			if r.Method == http.MethodPut {
				b, _ := io.ReadAll(r.Body)
				testament = string(b)
				return
			}
			if testament == "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = w.Write([]byte(testament))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	t.Setenv("SWEATSHOP_VICEROY_GEN", "3")
	ag := &stubAgent{}
	if err := viceroyRun([]string{"--heartbeat", "1h"}, ag); err != nil {
		t.Fatal(err)
	}
	if len(ag.prompts) != 1 || !strings.Contains(ag.prompts[0], "Generation: 3") || !strings.Contains(ag.prompts[0], "hi viceroy") {
		t.Fatalf("prompts: %q", ag.prompts)
	}
	if !strings.Contains(testament, "last-handled-message: 42") {
		t.Fatalf("testament %q", testament)
	}
	for _, bad := range [][]string{{"extra"}, {"--heartbeat", "0s"}} {
		if err := viceroyRun(bad, ag); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	t.Setenv("SWEATSHOP_LINE_TOKEN", "")
	if err := run([]string{"viceroy", "run"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "SWEATSHOP_LINE_TOKEN") {
		t.Fatalf("missing token: %v", err)
	}
}

func TestViceroySucceedAndCheckinCommands(t *testing.T) {
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" gen="+r.Header.Get("X-Sweatshop-Gen"))
		switch r.URL.Path {
		case "/v1/status":
			_, _ = w.Write([]byte(`{"master":"abc123"}`))
		case "/v1/viceroy/succeed":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"gen":4}`))
		case "/v1/viceroy/checkin":
			_, _ = w.Write([]byte(`{"gen":4}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	t.Setenv("SWEATSHOP_VICEROY_GEN", "3")
	var out bytes.Buffer
	if err := run([]string{"viceroy", "succeed"}, &out); err != nil || !strings.Contains(out.String(), `"gen":4`) {
		t.Fatalf("succeed: %q %v", out.String(), err)
	}
	if err := run([]string{"viceroy", "succeed", "--commit", "def456"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"viceroy", "checkin"}, &out); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /v1/status? gen=3",
		"POST /v1/viceroy/succeed?commit=abc123 gen=3",
		"POST /v1/viceroy/succeed?commit=def456 gen=3",
		"POST /v1/viceroy/checkin? gen=3",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(got, "\n"))
	}
	if err := run([]string{"viceroy", "checkin", "extra"}, &out); err == nil {
		t.Fatal("extra argument accepted")
	}
}

func TestViceroyRunChecksInWithPredecessor(t *testing.T) {
	var checkins int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/viceroy/checkin":
			checkins++
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"no succession is pending"}`))
		case "/v1/status":
			_, _ = w.Write([]byte(`{"master":"abc"}`))
		case "/v1/channel/inbox":
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case "/v1/testament":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	t.Setenv("SWEATSHOP_VICEROY_GEN", "2")
	t.Setenv("SWEATSHOP_PREDECESSOR", "abc")
	err := viceroyRun(nil, &stubAgent{keepRunning: true})
	if err == nil || !strings.Contains(err.Error(), "check in") || checkins != 1 {
		t.Fatalf("err=%v checkins=%d", err, checkins)
	}
}

func TestLineViceroyStartNeedsStation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "line")
	src := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", "--initial-branch=master"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "--quiet", "--allow-empty", "-m", "seed"}} {
		if out, err := exec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := run([]string{"line", "init", "--dir", dir, "--source", src}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"line", "viceroy", "start", "--dir", dir, "--commit", strings.Repeat("a", 40)}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no viceroy station") {
		t.Fatalf("err = %v", err)
	}
	if err := run([]string{"line", "viceroy", "start", "--dir", dir}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing --commit accepted")
	}
}

type failAgent struct{}

func (failAgent) Run(context.Context, string) (string, error) { return "", errors.New("boom") }

func TestViceroyRunNoCheckinWhenStartupFails(t *testing.T) {
	var checkins int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/viceroy/checkin":
			checkins++
		case "/v1/status":
			_, _ = w.Write([]byte(`{"master":"abc"}`))
		case "/v1/channel/inbox":
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case "/v1/testament":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	t.Setenv("SWEATSHOP_VICEROY_GEN", "2")
	t.Setenv("SWEATSHOP_PREDECESSOR", "abc")
	if err := viceroyRun(nil, failAgent{}); err == nil || checkins != 0 {
		t.Fatalf("err=%v checkins=%d", err, checkins)
	}
}

func TestLineConstitutionClient(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/constitution":
			_, _ = w.Write([]byte("toml\n"))
		case "/v1/constitution/log":
			_, _ = w.Write([]byte(`{"entries":[{"commit":"abcdef0123456789","message":"m"}]}`))
		case "/v1/constitution/proposals":
			b, _ := io.ReadAll(r.Body)
			if string(b) != "new" {
				w.WriteHeader(http.StatusBadRequest)
			}
			_, _ = w.Write([]byte(`{"proposal":4}`))
		}
	}))
	defer ts.Close()
	t.Setenv("SWEATSHOP_LINE_URL", ts.URL)
	t.Setenv("SWEATSHOP_LINE_TOKEN", "tok")
	var out bytes.Buffer
	if err := lineConstitution([]string{"get"}, nil, &out); err != nil || out.String() != "toml\n" {
		t.Fatalf("get %q %v", out.String(), err)
	}
	out.Reset()
	if err := lineConstitution([]string{"log"}, nil, &out); err != nil || out.String() != "abcdef012345 m\n" {
		t.Fatalf("log %q %v", out.String(), err)
	}
	out.Reset()
	if err := lineConstitution([]string{"propose"}, strings.NewReader("new"), &out); err != nil || !strings.Contains(out.String(), "proposal 4") {
		t.Fatalf("propose %q %v", out.String(), err)
	}
	if err := lineConstitution([]string{"bogus"}, nil, &out); err == nil {
		t.Fatal("bogus accepted")
	}
}

func TestLineInitGovernanceFlags(t *testing.T) {
	src := gitx.Repo{Dir: t.TempDir()}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=master"},
		{"config", "user.name", "t"}, {"config", "user.email", "t@example.com"},
		{"commit", "--quiet", "--allow-empty", "-m", "seed"},
	} {
		if _, err := src.Git(context.Background(), args...); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "line")
	err := run([]string{"line", "init", "--dir", dir, "--source", src.Dir,
		"--sovereign-name", "alice", "--sovereign-discord", "123", "--channel", "456"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := constitution.Load(filepath.Join(dir, "constitution.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Sovereign.Name != "alice" || c.Sovereign.Discord != "123" || c.Channel.Channel != "456" || c.Channel.Kind != "discord" {
		t.Fatalf("got %+v %+v", c.Sovereign, c.Channel)
	}
}
