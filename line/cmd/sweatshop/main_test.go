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
