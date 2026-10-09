package guard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/line/gitx"
)

func TestHookRejectsBranchPushesAndAllowsCandidates(t *testing.T) {
	f := newFixture(t)
	if err := InstallHooks(f.remote.Dir); err != nil {
		t.Fatal(err)
	}
	commit := f.commit("src/a.go", "package a")
	_, err := f.work.Git(f.ctx, "push", "origin", commit+":refs/heads/master")
	if err == nil || !strings.Contains(err.Error(), "submit through the witness") {
		t.Fatalf("direct push to master: err = %v", err)
	}
	if _, err := f.work.Git(f.ctx, "push", "--quiet", "origin", commit+":"+CandidatePrefix+commit); err != nil {
		t.Fatalf("candidate push rejected: %v", err)
	}
}

func TestInstallHooksIsIdempotentAndRefusesForeignHook(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := (gitx.Repo{Dir: filepath.Dir(dir)}).Git(ctx, "init", "--quiet", "--bare", dir); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(dir); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(dir); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(dir); err == nil {
		t.Fatal("overwrote a foreign pre-receive hook")
	}
}
