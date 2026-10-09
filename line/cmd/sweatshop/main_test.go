package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
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
