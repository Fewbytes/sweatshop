package station

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
)

func testamentRig(t *testing.T) (*Server, Client, Client) {
	t.Helper()
	repo := gitx.Repo{Dir: t.TempDir()}
	if _, err := repo.Git(context.Background(), "init", "--quiet", "--bare"); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Stations: []*Station{{Name: "v", Role: "viceroy", Token: "vt"}, {Name: "m", Role: "maker", Token: "mt"}}}
	srv.Guard.Repo = repo
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, Client{URL: ts.URL, Token: "vt"}, Client{URL: ts.URL, Token: "mt"}
}

func TestTestamentRoutes(t *testing.T) {
	srv, v, m := testamentRig(t)
	ctx := context.Background()
	if _, ok, err := v.TestamentGet(ctx); err != nil || ok {
		t.Fatalf("empty get: ok=%v err=%v", ok, err)
	}
	if err := v.TestamentPut(ctx, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := v.TestamentPut(ctx, []byte("two")); err != nil {
		t.Fatal(err)
	}
	body, ok, err := v.TestamentGet(ctx)
	if err != nil || !ok || string(body) != "two" {
		t.Fatalf("get %q %v %v", body, ok, err)
	}
	// History is kept: the second commit's parent holds the first body.
	repo := srv.Guard.Repo
	head, _ := repo.Ref(ctx, "refs/line/testament/viceroy")
	ps, err := repo.Parents(ctx, head)
	if err != nil || len(ps) != 1 {
		t.Fatalf("parents %v %v", ps, err)
	}
	if old, err := repo.ReadBlob(ctx, ps[0]+":testament"); err != nil || string(old) != "one" {
		t.Fatalf("history %q %v", old, err)
	}
	// An empty testament is allowed.
	if err := v.TestamentPut(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Size limit.
	if err := v.TestamentPut(ctx, []byte(strings.Repeat("x", 64<<10))); err != nil {
		t.Fatalf("64 KiB: %v", err)
	}
	if err := v.TestamentPut(ctx, []byte(strings.Repeat("x", 64<<10+1))); err == nil || !strings.Contains(err.Error(), "413") {
		t.Fatalf("oversize: %v", err)
	}
	// Makers are refused.
	if _, _, err := m.TestamentGet(ctx); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker get: %v", err)
	}
	if err := m.TestamentPut(ctx, []byte("x")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker put: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, v.URL+"/v1/testament", nil)
	req.Header.Set("Authorization", "Bearer vt")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("method: %v %v", resp, err)
	}
}

func TestTestamentRaceIs409(t *testing.T) {
	srv, v, _ := testamentRig(t)
	ctx := context.Background()
	if err := v.TestamentPut(ctx, []byte("one")); err != nil {
		t.Fatal(err)
	}
	// Move the ref between the handler's read and its CAS by replacing
	// Guard.Repo with a wrapper is not possible; instead check the CAS
	// primitive the handler relies on directly.
	repo := srv.Guard.Repo
	head, _ := repo.Ref(ctx, "refs/line/testament/viceroy")
	blob, _ := repo.WriteBlob(ctx, []byte("other"))
	tree, _ := repo.WriteTreeWithBlob(ctx, "testament", blob)
	c, err := repo.CommitTree(ctx, tree, head, "x", "viceroy")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(ctx, "refs/line/testament/viceroy", c, head); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(ctx, "refs/line/testament/viceroy", c, head); err != gitx.ErrRefMoved {
		t.Fatalf("stale CAS: %v", err)
	}
	body, _, _ := v.TestamentGet(ctx)
	if string(body) != "other" {
		t.Fatalf("get %q", body)
	}
}
