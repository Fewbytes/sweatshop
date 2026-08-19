package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	agentrpc "github.com/Fewbytes/sweatshop/agentsh/internal/rpc"
	"github.com/Fewbytes/sweatshop/agentsh/internal/workspace"
)

func TestRunDoctorReportsProblemsWhenNothingIsSetUp(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTSHD_PATH", filepath.Join(root, "does-not-exist"))
	t.Setenv("PATH", root) // an empty-of-agentshd PATH

	if runDoctor(paths) {
		t.Fatal("expected runDoctor to report a problem when agentshd is missing and nothing is running")
	}
}

func TestRunDoctorAllHealthyAgainstLiveDaemon(t *testing.T) {
	root := t.TempDir()
	paths, err := workspace.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}

	agentshdBin := filepath.Join(root, "agentshd")
	build := exec.Command("go", "build", "-o", agentshdBin, "../agentshd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building agentshd: %v\n%s", err, out)
	}
	t.Setenv("AGENTSHD_PATH", agentshdBin)

	if err := startDaemon(paths); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client := agentrpc.Client{Socket: paths.Socket, Timeout: 5 * time.Second}
		_ = client.Call(context.Background(), agentrpc.Request{ID: "shutdown", Op: agentrpc.OpShutdown}, nil)
	})

	if !runDoctor(paths) {
		t.Fatal("expected runDoctor to report all-clear against a live, matching-version daemon")
	}
}
