// Package acceptance holds black-box tests that drive the built sweatshop
// binary. Only role gemba may change this tree (see the acceptance/** guard
// written by line init); makers satisfy these tests, they do not edit them.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// TestLineInitThenStationStatus initialises a line from a throwaway repository,
// serves its station API, and checks an authorised status call succeeds while
// an unauthenticated one is refused.
func TestLineInitThenStationStatus(t *testing.T) {
	work := t.TempDir()
	bin := filepath.Join(work, "sweatshop")
	run(t, "../line", "go", "build", "-o", bin, "./cmd/sweatshop")

	source := filepath.Join(work, "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, source, "git", "init", "--quiet", "--initial-branch=master")
	if err := os.WriteFile(filepath.Join(source, "README"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, source, "git", "add", "README")
	run(t, source, "git", "-c", "user.name=acceptance", "-c", "user.email=acceptance@example.com", "commit", "--quiet", "-m", "seed")

	lineDir := filepath.Join(work, "line")
	run(t, work, bin, "line", "init", "--dir", lineDir, "--source", source)

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	serve := exec.CommandContext(ctx, bin, "line", "serve", "--dir", lineDir, "--addr", addr)
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = serve.Wait()
	})

	token, err := os.ReadFile(filepath.Join(lineDir, "stations", "maker-1", "token"))
	if err != nil {
		t.Fatal(err)
	}
	get := func(bearer string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, fmt.Sprint(body)
	}

	deadline := time.Now().Add(30 * time.Second)
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		code, body := get(strings.TrimSpace(string(token)))
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("authorised GET /v1/status never succeeded; last: %d %s", code, body)
		}
		<-poll.C
	}
	if code, body := get("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("bad token GET /v1/status = %d %s, want 401", code, body)
	}
}
