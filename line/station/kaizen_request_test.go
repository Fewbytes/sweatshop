package station

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
	"github.com/Fewbytes/sweatshop/line/guard"
)

type kaizenRig struct {
	srv                   *Server
	repo                  gitx.Repo
	heijunka, kaizen, mkr Client
}

func newKaizenRig(t *testing.T) *kaizenRig {
	t.Helper()
	ctx := context.Background()
	repo := gitx.Repo{Dir: t.TempDir()}
	ok(t)(repo.Git(ctx, "init", "--quiet", "--initial-branch=master"))
	ok(t)(repo.Git(ctx, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "seed"))
	srv := &Server{
		Guard: guard.Guard{Repo: repo, Ref: "refs/heads/master"},
		Stations: []*Station{
			{Name: "h", Role: "heijunka", Token: "ht"},
			{Name: "k", Role: "kaizen", Token: "kt"},
			{Name: "m", Role: "maker", Token: "mt"},
		},
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &kaizenRig{srv: srv, repo: repo,
		heijunka: Client{URL: ts.URL, Token: "ht"}, kaizen: Client{URL: ts.URL, Token: "kt"}, mkr: Client{URL: ts.URL, Token: "mt"}}
}

func (r *kaizenRig) request(t *testing.T) *KaizenRequest {
	t.Helper()
	st, err := r.heijunka.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st.KaizenRequest
}

func TestKaizenRequestIsPendingOnTheMasterItWasMadeOn(t *testing.T) {
	r := newKaizenRig(t)
	ctx := context.Background()
	if got := r.request(t); got != nil {
		t.Fatalf("before any request: %+v", got)
	}
	if err := r.heijunka.RequestKaizen(ctx); err != nil {
		t.Fatal(err)
	}
	master, err := r.repo.Ref(ctx, "refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.request(t); got == nil || got.State != KaizenRequestPending || got.Master != master {
		t.Fatalf("got %+v, want pending on %s", got, master)
	}
}

func TestKaizenRequestRefusedWhilePendingOrCordPulled(t *testing.T) {
	r := newKaizenRig(t)
	ctx := context.Background()
	if err := r.heijunka.RequestKaizen(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.heijunka.RequestKaizen(ctx); err == nil || !strings.Contains(err.Error(), "already pending") {
		t.Fatalf("second request: %v", err)
	}
	if err := r.kaizen.AnswerKaizen(ctx, KaizenAnswer{State: KaizenRequestStarted}); err != nil {
		t.Fatal(err)
	}
	master, _ := r.repo.Ref(ctx, "refs/heads/master")
	ok(t)(r.repo.Git(ctx, "update-ref", guard.StopRef, master))
	if err := r.heijunka.RequestKaizen(ctx); err == nil || !strings.Contains(err.Error(), "andon cord is pulled") {
		t.Fatalf("request while stopped: %v", err)
	}
}

func TestKaizenAnswerIsRecordedInStatus(t *testing.T) {
	r := newKaizenRig(t)
	ctx := context.Background()
	if err := r.kaizen.AnswerKaizen(ctx, KaizenAnswer{State: KaizenRequestStarted}); err == nil || !strings.Contains(err.Error(), "no kaizen request is pending") {
		t.Fatalf("answer with nothing pending: %v", err)
	}
	if err := r.heijunka.RequestKaizen(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.kaizen.AnswerKaizen(ctx, KaizenAnswer{State: KaizenRequestDeclined, Reason: "master has not moved"}); err != nil {
		t.Fatal(err)
	}
	if got := r.request(t); got.State != KaizenRequestDeclined || got.Reason != "master has not moved" {
		t.Fatalf("got %+v", got)
	}
	if err := r.heijunka.RequestKaizen(ctx); err != nil {
		t.Fatalf("a new request after an answer: %v", err)
	}
}

func TestKaizenAnswerRejectsBadBodies(t *testing.T) {
	r := newKaizenRig(t)
	ctx := context.Background()
	if err := r.heijunka.RequestKaizen(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range []KaizenAnswer{{State: KaizenRequestPending}, {State: KaizenRequestDeclined}, {State: KaizenRequestStarted, Reason: "x"}} {
		if err := r.kaizen.AnswerKaizen(ctx, a); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("%+v: %v", a, err)
		}
	}
}

func TestKaizenRoutesAreRoleGated(t *testing.T) {
	r := newKaizenRig(t)
	ctx := context.Background()
	if err := r.mkr.RequestKaizen(ctx); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker request: %v", err)
	}
	if err := r.heijunka.RequestKaizen(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.heijunka.AnswerKaizen(ctx, KaizenAnswer{State: KaizenRequestStarted}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("heijunka answer: %v", err)
	}
}
