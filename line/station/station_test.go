package station

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/constitution"
	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
	"github.com/Fewbytes/sweatshop/line/linedir"
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

// The check fails, with output, exactly when BROKEN exists.
const checkScript = "test ! -e BROKEN || { echo 'BROKEN is present' >&2; exit 1; }\n"

type rig struct {
	t            *testing.T
	ctx          context.Context
	line         linedir.Line
	srv          *Server
	ts           *httptest.Server
	agent        gitx.Repo
	maker, andon Client
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	src := gitx.Repo{Dir: t.TempDir()}
	ok(t)(src.Git(ctx, "init", "--quiet", "--initial-branch=master"))
	ok(t)(src.Git(ctx, "config", "user.name", "t"))
	ok(t)(src.Git(ctx, "config", "user.email", "t@example.com"))
	if err := os.WriteFile(filepath.Join(src.Dir, "check.sh"), []byte(checkScript), 0o644); err != nil {
		t.Fatal(err)
	}
	ok(t)(src.Git(ctx, "add", "check.sh"))
	ok(t)(src.Git(ctx, "commit", "--quiet", "-m", "seed"))

	l, err := linedir.Init(ctx, linedir.InitOptions{Dir: filepath.Join(t.TempDir(), "line"), Source: src.Dir, Check: "sh check.sh"})
	if err != nil {
		t.Fatal(err)
	}
	// Host-local checks: trust level none, keeping the generated keys and roles.
	c, _, err := constitution.Load(l.Constitution)
	if err != nil {
		t.Fatal(err)
	}
	c.Trust.Level = "none"
	c.Runner = constitution.Runner{Kind: "local"}
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Constitution, data, 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := NewFromLine(l)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	agent := gitx.Repo{Dir: filepath.Join(t.TempDir(), "agent")}
	ok(t)(src.Git(ctx, "clone", "--quiet", l.Remote, agent.Dir))
	ok(t)(agent.Git(ctx, "config", "user.name", "t"))
	ok(t)(agent.Git(ctx, "config", "user.email", "t@example.com"))

	r := &rig{t: t, ctx: ctx, line: l, srv: srv, ts: ts, agent: agent}
	r.maker = Client{URL: ts.URL, Token: r.token("maker-1")}
	r.andon = Client{URL: ts.URL, Token: r.token("andon-1")}
	return r
}

func (r *rig) token(name string) string {
	r.t.Helper()
	for _, s := range r.line.Stations {
		if s.Name == name {
			tok, err := s.Token()
			if err != nil {
				r.t.Fatal(err)
			}
			return tok
		}
	}
	r.t.Fatalf("no station %s", name)
	return ""
}

// commit writes (or, with content "", removes) name and commits with msg.
func (r *rig) commit(name, content, msg string) string {
	r.t.Helper()
	full := filepath.Join(r.agent.Dir, name)
	if content == "" {
		if err := os.Remove(full); err != nil {
			r.t.Fatal(err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	ok(r.t)(r.agent.Git(r.ctx, "add", "-A"))
	ok(r.t)(r.agent.Git(r.ctx, "commit", "--quiet", "-m", msg))
	sha, err := r.agent.Commit(r.ctx, "HEAD")
	if err != nil {
		r.t.Fatal(err)
	}
	return sha
}

func (r *rig) master() string {
	r.t.Helper()
	sha, err := (gitx.Repo{Dir: r.line.Remote}).Commit(r.ctx, "refs/heads/master")
	if err != nil {
		r.t.Fatal(err)
	}
	return sha
}

func (r *rig) raw(method, path, token string, body []byte) (int, string) {
	r.t.Helper()
	req, err := http.NewRequest(method, r.ts.URL+path, bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func TestUnauthorized(t *testing.T) {
	r := newRig(t)
	for _, tok := range []string{"", "not-a-token", r.token("maker-1") + "x"} {
		for _, route := range [][2]string{{"GET", "/v1/status"}, {"POST", "/v1/attest-line?commit=" + r.master()}, {"GET", "/v1/nope"}} {
			code, body := r.raw(route[0], route[1], tok, nil)
			if code != http.StatusUnauthorized {
				t.Fatalf("%s %s with token %q: %d %s", route[0], route[1], tok, code, body)
			}
			var e struct{ Error string }
			if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error != "unauthorized" {
				t.Fatalf("body = %q", body)
			}
		}
	}
	if _, err := (Client{URL: r.ts.URL, Token: "wrong"}).Status(r.ctx); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("client with wrong token: %v", err)
	}
}

func TestMergeGoodCommit(t *testing.T) {
	r := newRig(t)
	head := r.commit("src/a.txt", "a", "add a")
	res, err := r.maker.Merge(r.ctx, r.agent.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Guard.Status != guard.Merged || !res.Attestation.Passed || res.Attestation.Commit != head {
		t.Fatalf("result = %+v", res)
	}
	if got := r.master(); got != head {
		t.Fatalf("master = %s, want %s", got, head)
	}
	st, err := r.maker.Status(r.ctx)
	if err != nil || st.Master != head || st.Event != "" {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestMergeBrokenCommit(t *testing.T) {
	r := newRig(t)
	before := r.master()
	r.commit("BROKEN", "x", "break")
	res, err := r.maker.Merge(r.ctx, r.agent.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Attestation.Passed || res.Guard.Status != guard.Rejected || res.Guard.Reason != guard.ReasonChecksFailed {
		t.Fatalf("result = %+v", res)
	}
	if got := r.master(); got != before {
		t.Fatalf("master moved to %s", got)
	}
	checks := res.Attestation.Statement.Predicate.Checks
	if len(checks) != 1 || checks[0].EvidenceID == "" {
		t.Fatalf("checks = %+v", checks)
	}
	// Any station may read any station's evidence.
	log, err := r.andon.Evidence(r.ctx, checks[0].EvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "BROKEN is present") {
		t.Fatalf("evidence = %q", log)
	}
	if _, err := r.andon.Evidence(r.ctx, "ev_0000000000000000"); err == nil {
		t.Fatal("missing evidence returned no error")
	}
	if code, _ := r.raw("GET", "/v1/evidence/..%2f..%2ftoken", r.token("maker-1"), nil); code == http.StatusOK {
		t.Fatal("served a path outside the evidence directory")
	}
}

func TestAttestOnly(t *testing.T) {
	r := newRig(t)
	before := r.master()
	head := r.commit("src/b.txt", "b", "add b")
	res, err := r.maker.Attest(r.ctx, r.agent.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || res.Commit != head || res.Envelope == nil {
		t.Fatalf("result = %+v", res)
	}
	if r.master() != before {
		t.Fatal("attest moved master")
	}
}

func TestBadCommitParam(t *testing.T) {
	r := newRig(t)
	tok := r.token("maker-1")
	for _, path := range []string{
		"/v1/attest?commit=abc123",
		"/v1/merge?commit=HEAD",
		"/v1/attest-line?commit=" + strings.ToUpper(r.master()),
		"/v1/attest-line",
		"/v1/attest?commit=--upload-pack=x",
	} {
		if code, body := r.raw("POST", path, tok, []byte("x")); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	if _, err := r.maker.AttestLine(r.ctx, "HEAD"); err == nil {
		t.Error("client AttestLine accepted HEAD")
	}
}

func TestOversizedBundle(t *testing.T) {
	r := newRig(t)
	r.srv.MaxBundle = 1024
	path := "/v1/attest?commit=" + r.master()
	if code, body := r.raw("POST", path, r.token("maker-1"), bytes.Repeat([]byte("x"), 2048)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: %d %s", code, body)
	}
	// Without a Content-Length the limit is enforced while streaming.
	req, err := http.NewRequest("POST", r.ts.URL+path, io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("x"), 4096))))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token("maker-1"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.ContentLength != 0 || resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized: content-length %d, status %d", req.ContentLength, resp.StatusCode)
	}
	entries, err := os.ReadDir(r.line.Stations[0].StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bundle") {
			t.Fatalf("upload left behind: %s", e.Name())
		}
	}
}

func TestBadBundleIs422(t *testing.T) {
	r := newRig(t)
	code, body := r.raw("POST", "/v1/attest?commit="+r.master(), r.token("maker-1"), []byte("not a bundle"))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "error") {
		t.Fatalf("garbage bundle: %d %s", code, body)
	}
}

func TestWrongMethodAndUnknownRoute(t *testing.T) {
	r := newRig(t)
	tok := r.token("maker-1")
	if code, _ := r.raw("GET", "/v1/attest?commit="+r.master(), tok, nil); code != http.StatusMethodNotAllowed {
		t.Errorf("GET attest: %d", code)
	}
	if code, _ := r.raw("GET", "/v1/nope", tok, nil); code != http.StatusNotFound {
		t.Errorf("unknown route: %d", code)
	}
	if code, _ := r.raw("POST", "/v1/andon/pull", tok, []byte("{not json")); code != http.StatusBadRequest {
		t.Errorf("bad envelope json: %d", code)
	}
}

// landBroken puts a BROKEN commit on master through the guard directly, with
// a passing attestation forged under the maker's (trusted) key.
func (r *rig) landBroken() string {
	r.t.Helper()
	broken := r.commit("BROKEN", "x", "break")
	ok(r.t)(r.agent.Git(r.ctx, "push", "--quiet", "origin", broken+":"+guard.CandidatePrefix+broken))
	var maker linedir.Station
	for _, s := range r.line.Stations {
		if s.Name == "maker-1" {
			maker = s
		}
	}
	key, err := attest.LoadPrivateKey(maker.KeyFile)
	if err != nil {
		r.t.Fatal(err)
	}
	tree, err := r.agent.Tree(r.ctx, broken)
	if err != nil {
		r.t.Fatal(err)
	}
	st := attest.NewStatement(broken, tree, attest.ChecksPredicate{
		Role: "maker", AgentID: "maker-1", Constitution: r.srv.Guard.ConstitutionDigest, Runner: "local",
		Checks: []attest.CheckResult{{Name: "ci", Command: "sh check.sh", ExitCode: 0}},
	})
	env, err := attest.SignStatement(key, st)
	if err != nil {
		r.t.Fatal(err)
	}
	res, err := r.srv.Guard.Merge(r.ctx, broken, env)
	if err != nil || res.Status != guard.Merged {
		r.t.Fatalf("forged merge = %+v, %v", res, err)
	}
	ok(r.t)(r.agent.Git(r.ctx, "fetch", "--quiet", "origin"))
	return broken
}

func TestAndonFlow(t *testing.T) {
	r := newRig(t)
	broken := r.landBroken()

	failing, err := r.andon.AttestLine(r.ctx, broken)
	if err != nil {
		t.Fatal(err)
	}
	if failing.Passed || failing.Envelope == nil {
		t.Fatalf("attest broken master = %+v", failing)
	}
	pulled, err := r.andon.Pull(r.ctx, *failing.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if pulled.Status != guard.Stopped || pulled.Event == "" {
		t.Fatalf("pull = %+v", pulled)
	}
	st, err := r.maker.Status(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Event != pulled.Event || st.Andon == nil || st.Andon.Commit != broken || st.Master != broken {
		t.Fatalf("status = %+v", st)
	}

	// An untagged change cannot merge while stopped.
	ok(t)(r.agent.Git(r.ctx, "rm", "--quiet", "BROKEN"))
	ok(t)(r.agent.Git(r.ctx, "commit", "--quiet", "-m", "untagged fix"))
	res, err := r.maker.Merge(r.ctx, r.agent.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Guard.Reason != guard.ReasonLineStopped {
		t.Fatalf("untagged merge while stopped = %+v", res)
	}
	ok(t)(r.agent.Git(r.ctx, "commit", "--quiet", "--amend", "-m", "fix\n\n"+guard.FixTrailer+": "+pulled.Event))
	fix, err := r.agent.Commit(r.ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	res, err = r.maker.Merge(r.ctx, r.agent.Dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.Guard.Status != guard.Merged || r.master() != fix {
		t.Fatalf("tagged fix = %+v", res)
	}

	passing, err := r.andon.AttestLine(r.ctx, fix)
	if err != nil || !passing.Passed {
		t.Fatalf("attest fixed master = %+v, %v", passing, err)
	}
	cleared, err := r.andon.Clear(r.ctx, *passing.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Status != guard.Cleared {
		t.Fatalf("clear = %+v", cleared)
	}
	if st, err := r.andon.Status(r.ctx); err != nil || st.Event != "" || st.Andon != nil {
		t.Fatalf("status after clear = %+v, %v", st, err)
	}
}

func TestNewFromLineRejectsSharedTokens(t *testing.T) {
	r := newRig(t)
	a, b := r.line.Stations[0], r.line.Stations[1]
	data, err := os.ReadFile(a.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.TokenFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromLine(r.line); err == nil {
		t.Fatal("two stations sharing a token were accepted")
	}
}
