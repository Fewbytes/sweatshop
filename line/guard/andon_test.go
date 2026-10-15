package guard

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/Fewbytes/sweatshop/line/gitx/gittest"
)

func (f *fixture) failing(commit string) attest.Envelope {
	return f.envelope(commit, f.key, func(p *attest.ChecksPredicate) { p.Checks[0].ExitCode = 2 })
}

func (f *fixture) passing(commit string) attest.Envelope {
	return f.envelope(commit, f.key, nil)
}

// commitMsg is commit with a caller-chosen message, for trailers.
func (f *fixture) commitMsg(name, content, msg string) string {
	f.t.Helper()
	full := filepath.Join(f.work.Dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	ok(f.t)(gittest.Git(f.ctx, f.work.Dir, "add", "-A"))
	ok(f.t)(gittest.Git(f.ctx, f.work.Dir, "commit", "--quiet", "-m", msg))
	sha, err := f.work.Commit(f.ctx, "HEAD")
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) pull(env attest.Envelope) Result {
	f.t.Helper()
	r, err := f.guard.Pull(f.ctx, env)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) clear(env attest.Envelope) Result {
	f.t.Helper()
	r, err := f.guard.Clear(f.ctx, env)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) expectReason(r Result, reason Reason) {
	f.t.Helper()
	if r.Status != Rejected || r.Reason != reason {
		f.t.Fatalf("result = %+v, want rejected/%s", r, reason)
	}
}

func (f *fixture) merge(commit string, env attest.Envelope) Result {
	f.t.Helper()
	f.candidate(commit)
	r, err := f.guard.Merge(f.ctx, commit, env)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) stopRefExists() bool {
	f.t.Helper()
	exists, err := f.remote.RefExists(f.ctx, StopRef)
	if err != nil {
		f.t.Fatal(err)
	}
	return exists
}

func TestPullStopsTheLine(t *testing.T) {
	f := newFixture(t)
	master := f.master()
	r := f.pull(f.failing(master))
	if r.Status != Stopped || r.Event == "" || r.Commit != master {
		t.Fatalf("pull = %+v", r)
	}
	id, event, err := f.guard.Stopped(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id != r.Event || event == nil || event.Check != "ci" || event.ExitCode != 2 || event.Commit != master || event.EvidenceID != "inv_1" {
		t.Fatalf("stopped = %s %+v", id, event)
	}
	commit := f.commit("src/a.go", "package a")
	if r := f.merge(commit, f.passing(commit)); r.Status != Rejected || r.Reason != ReasonLineStopped || r.Event != id {
		t.Fatalf("merge = %+v", r)
	}
	if f.master() != master {
		t.Fatal("master moved while stopped")
	}
}

func TestPullRejectsPassingEvidence(t *testing.T) {
	f := newFixture(t)
	f.expectReason(f.pull(f.passing(f.master())), ReasonNoFailure)
	if f.stopRefExists() {
		t.Fatal("stop ref written for passing evidence")
	}
}

func TestPullRejectsStaleEvidence(t *testing.T) {
	f := newFixture(t)
	stale := f.failing(f.master())
	commit := f.commit("src/a.go", "package a")
	if r := f.merge(commit, f.passing(commit)); r.Status != Merged {
		t.Fatalf("merge = %+v", r)
	}
	r := f.pull(stale)
	f.expectReason(r, ReasonStaleEvidence)
	if r.Base != commit || f.stopRefExists() {
		t.Fatalf("result = %+v, stop ref = %v", r, f.stopRefExists())
	}
}

func TestPullRejectsUntrustedKey(t *testing.T) {
	f := newFixture(t)
	_, attacker, _ := ed25519.GenerateKey(rand.Reader)
	f.expectReason(f.pull(f.envelope(f.master(), attacker, func(p *attest.ChecksPredicate) { p.Checks[0].ExitCode = 2 })), ReasonUntrusted)
	if f.stopRefExists() {
		t.Fatal("stop ref written for untrusted evidence")
	}
}

func TestSecondPullReportsExistingEvent(t *testing.T) {
	f := newFixture(t)
	first := f.pull(f.failing(f.master()))
	second := f.pull(f.failing(f.master()))
	f.expectReason(second, ReasonAlreadyStopped)
	if second.Event != first.Event {
		t.Fatalf("event %s, want %s", second.Event, first.Event)
	}
}

func TestFixWithTrailerMergesWhileStopped(t *testing.T) {
	f := newFixture(t)
	id := f.pull(f.failing(f.master())).Event

	wrong := f.commitMsg("src/a.go", "package a", "fix\n\nAndon-Event: "+f.master())
	if r := f.merge(wrong, f.passing(wrong)); r.Reason != ReasonLineStopped {
		t.Fatalf("wrong trailer: %+v", r)
	}
	ok(t)(gittest.Git(f.ctx, f.work.Dir, "reset", "--quiet", "--hard", f.master()))

	fix := f.commitMsg("src/a.go", "package a", "fix\n\nAndon-Event: "+id)
	if r := f.merge(fix, f.passing(fix)); r.Status != Merged {
		t.Fatalf("fix: %+v", r)
	}
}

func TestClearNeedsPassingMaster(t *testing.T) {
	f := newFixture(t)
	id := f.pull(f.failing(f.master())).Event
	fix := f.commitMsg("src/a.go", "package a", "fix\n\nAndon-Event: "+id)
	if r := f.merge(fix, f.passing(fix)); r.Status != Merged {
		t.Fatalf("fix: %+v", r)
	}
	f.expectReason(f.clear(f.failing(fix)), ReasonChecksFailed)
	if !f.stopRefExists() {
		t.Fatal("stop ref cleared by failing evidence")
	}
	r := f.clear(f.passing(fix))
	if r.Status != Cleared || r.Event != id {
		t.Fatalf("clear = %+v", r)
	}
	if f.stopRefExists() {
		t.Fatal("stop ref left behind")
	}
	next := f.commit("src/b.go", "package b")
	if r := f.merge(next, f.passing(next)); r.Status != Merged {
		t.Fatalf("merge after clear: %+v", r)
	}
}

func TestClearWhenRunning(t *testing.T) {
	f := newFixture(t)
	f.expectReason(f.clear(f.passing(f.master())), ReasonNotStopped)
}

func TestClearRejectsStaleEvidence(t *testing.T) {
	f := newFixture(t)
	m1 := f.master()
	id := f.pull(f.failing(m1)).Event
	fix := f.commitMsg("src/a.go", "package a", "fix\n\nAndon-Event: "+id)
	if r := f.merge(fix, f.passing(fix)); r.Status != Merged {
		t.Fatalf("fix: %+v", r)
	}
	f.expectReason(f.clear(f.passing(m1)), ReasonStaleEvidence)
	if !f.stopRefExists() {
		t.Fatal("stop ref cleared by stale evidence")
	}
}

// The merge attestation of the failing master is a passing run of that same
// commit; replaying it must not clear the line without a fix.
func TestClearRejectsReplayOfPulledCommit(t *testing.T) {
	f := newFixture(t)
	m := f.master()
	id := f.pull(f.failing(m)).Event
	r := f.clear(f.passing(m))
	f.expectReason(r, ReasonStaleEvidence)
	if r.Detail != "clearing needs a passing run on a master newer than the failing commit" || !f.stopRefExists() {
		t.Fatalf("result = %+v", r)
	}
	fix := f.commitMsg("src/a.go", "package a", "fix\n\nAndon-Event: "+id)
	if r := f.merge(fix, f.passing(fix)); r.Status != Merged {
		t.Fatalf("fix: %+v", r)
	}
	if r := f.clear(f.passing(fix)); r.Status != Cleared {
		t.Fatalf("clear = %+v", r)
	}
}

func TestPullRecordsAttestationDigest(t *testing.T) {
	f := newFixture(t)
	env := f.failing(f.master())
	f.pull(env)
	_, event, err := f.guard.Stopped(f.ctx)
	if err != nil || event == nil {
		t.Fatalf("stopped: %+v %v", event, err)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if want := "sha256:" + hex.EncodeToString(sum[:]); event.Attestation != want {
		t.Fatalf("attestation = %q, want %q", event.Attestation, want)
	}
}

func TestTaggedFixStillNeedsPassingChecks(t *testing.T) {
	f := newFixture(t)
	id := f.pull(f.failing(f.master())).Event
	before := f.master()
	fix := f.commitMsg("src/a.go", "package a", "fix\n\nAndon-Event: "+id)
	if r := f.merge(fix, f.failing(fix)); r.Status != Rejected || r.Reason != ReasonChecksFailed {
		t.Fatalf("fix: %+v", r)
	}
	if f.master() != before {
		t.Fatal("master moved on a failing fix")
	}
}

func TestPullHonoursKeyRoles(t *testing.T) {
	f := newFixture(t)
	f.guard.Constitution.Trust.Keys[0].Roles = []string{"gemba"}
	f.expectReason(f.pull(f.failing(f.master())), ReasonRoleNotPermitted)
	if f.stopRefExists() {
		t.Fatal("stop ref written despite role binding")
	}
}

// A hand-written stop ref (no event blob) still stops the line.
func TestUndecodableStopRefStillStops(t *testing.T) {
	f := newFixture(t)
	ok(t)(gittest.Git(f.ctx, f.remote.Dir, "update-ref", StopRef, f.master()))
	id, event, err := f.guard.Stopped(f.ctx)
	if err != nil || id != f.master() || event != nil {
		t.Fatalf("stopped = %s %+v %v", id, event, err)
	}
	f.expectReason(f.pull(f.failing(f.master())), ReasonAlreadyStopped)
}
