package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformSupportedMatchesDeclaredMatrix(t *testing.T) {
	type target struct{ goos, goarch string }
	cases := map[target]bool{
		{"linux", "amd64"}:  true,
		{"linux", "arm64"}:  true,
		{"darwin", "arm64"}: true,
		// go-libsql has no prebuilt static lib for darwin_amd64 — it can't
		// even be built, let alone run.
		{"darwin", "amd64"}:  false,
		{"windows", "amd64"}: false,
		{"plan9", "amd64"}:   false,
	}
	for tgt, want := range cases {
		if got := platformSupported(tgt.goos, tgt.goarch); got != want {
			t.Errorf("platformSupported(%q, %q) = %v, want %v", tgt.goos, tgt.goarch, got, want)
		}
	}
}

func TestVersionFlagPrintsVersionWithoutStartingADaemon(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "agentshd")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building agentshd: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "agentshd") {
		t.Fatalf("--version output = %q, want it to contain \"agentshd\"", out)
	}
}
