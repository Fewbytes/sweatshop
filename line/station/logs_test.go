package station

import (
	"context"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/linelog"
	"github.com/Fewbytes/sweatshop/line/linestore"
)

func logsRig(t *testing.T) (srv *Server, maker, viceroy Client) {
	t.Helper()
	store, err := linestore.Open(context.Background(), filepath.Join(t.TempDir(), linestore.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv = &Server{Store: store, Stations: []*Station{
		{Name: "v", Role: "viceroy", Token: "vt-secret"},
		{Name: "m", Role: "maker", Token: "mt-secret"},
	}}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, Client{URL: ts.URL, Token: "mt-secret"}, Client{URL: ts.URL, Token: "vt-secret"}
}

func TestOnlyViceroyReadsLogsAndAnyStationPostsItsOwn(t *testing.T) {
	_, maker, viceroy := logsRig(t)
	ctx := context.Background()
	if err := maker.PostLogs(ctx, []string{"maker: gave up x-1: checks failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := maker.Logs(ctx, "", ""); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("maker read logs: err = %v, want 403", err)
	}
	got, err := viceroy.Logs(ctx, "1h", "m")
	if err != nil || len(got) != 1 || got[0].Source != "m" || got[0].Line != "maker: gave up x-1: checks failed" {
		t.Fatalf("Logs = %+v, %v; want the maker's line attributed to station m", got, err)
	}
	if got, _ := viceroy.Logs(ctx, "", "other"); len(got) != 0 {
		t.Fatalf("station filter returned %+v", got)
	}
	if err := maker.PostLogs(ctx, make([]string, maxLogPostLines+1)); err == nil {
		t.Fatal("an oversized post was accepted")
	}
}

func TestLogsNeverShowStationTokensOrCredentials(t *testing.T) {
	srv, maker, viceroy := logsRig(t)
	ctx := context.Background()
	if err := maker.PostLogs(ctx, []string{"dial failed with vt-secret and Bearer abcdef0123456789"}); err != nil {
		t.Fatal(err)
	}
	// A line stored before redaction existed: the read path still hides it.
	if err := srv.Store.RecordLog(ctx, linestore.LogLine{Source: "serve", Line: "leaked mt-secret"}); err != nil {
		t.Fatal(err)
	}
	got, err := viceroy.Logs(ctx, "", "")
	if err != nil || len(got) != 2 {
		t.Fatalf("Logs = %+v, %v", got, err)
	}
	for _, l := range got {
		if strings.Contains(l.Line, "secret") || strings.Contains(l.Line, "abcdef0123") {
			t.Errorf("log line leaks a secret: %q", l.Line)
		}
	}
	if got[0].Line != "dial failed with "+linelog.Placeholder+" and Bearer "+linelog.Placeholder {
		t.Errorf("line = %q", got[0].Line)
	}
}

// The station API's own request log must not carry the caller's token, so a
// reader never depends on redaction to be safe.
func TestRequestLogLinesDoNotContainTheCallersToken(t *testing.T) {
	srv, maker, viceroy := logsRig(t)
	ctx := context.Background()
	log.SetOutput(linelog.Tee{Out: &strings.Builder{}, Record: func(e string) {
		if err := srv.Store.RecordLog(ctx, linestore.LogLine{Source: ServeLogSource, Line: e}); err != nil {
			t.Error(err)
		}
	}}) // no Secrets: only the code's own care keeps the token out
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if _, err := maker.Events(ctx, "", "", ""); err != nil {
		t.Fatal(err)
	}
	bad := Client{URL: maker.URL, Token: "forged-token-value"}
	if _, err := bad.Events(ctx, "", "", ""); err == nil {
		t.Fatal("forged token accepted")
	}
	got, err := viceroy.Logs(ctx, "", ServeLogSource)
	if err != nil || len(got) < 2 {
		t.Fatalf("Logs = %+v, %v; want the request lines", got, err)
	}
	for _, l := range got {
		if strings.Contains(l.Line, "mt-secret") || strings.Contains(l.Line, "forged-token-value") {
			t.Errorf("request log line leaks a token: %q", l.Line)
		}
	}
	if !strings.Contains(got[0].Line, "station=m ") || !strings.Contains(got[1].Line, "outcome=unauthorized") {
		t.Errorf("lines do not describe the requests: %+v", got)
	}
}
