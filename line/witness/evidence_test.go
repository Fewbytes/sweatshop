package witness

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogRunnerCapturesOutputAndExitCode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ev")
	r := LogRunner{Dir: dir}
	res, err := r.Run(context.Background(), t.TempDir(), "echo out; echo err >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || !strings.HasPrefix(res.EvidenceID, "ev_") {
		t.Fatalf("result = %+v", res)
	}
	data, err := ReadEvidence(dir, res.EvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "out") || !strings.Contains(string(data), "err") {
		t.Fatalf("log = %q", data)
	}
	info, _ := os.Stat(filepath.Join(dir, res.EvidenceID+".log"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(dir)
	if dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dinfo.Mode().Perm())
	}
}

func TestReadEvidenceRejectsBadIDs(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../x", "ev_../../x", "ev_0123", "ev_0123456789abcdeg", "ev_0123456789ABCDEF", "", "ev_0123456789abcdef/../x"} {
		if _, err := ReadEvidence(dir, id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}

func TestLogRunnerKeepsTailOfLongOutput(t *testing.T) {
	dir := t.TempDir()
	r := LogRunner{Dir: dir}
	res, err := r.Run(context.Background(), t.TempDir(), "head -c 5000000 /dev/zero | tr '\\0' a; echo; echo THE-END")
	if err != nil {
		t.Fatal(err)
	}
	data, err := ReadEvidence(dir, res.EvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("[truncated]\n")) || !bytes.HasSuffix(data, []byte("THE-END\n")) {
		t.Fatalf("not a truncated tail: %d bytes", len(data))
	}
	if len(data) != maxEvidence+len("[truncated]\n") {
		t.Fatalf("len = %d", len(data))
	}
}
