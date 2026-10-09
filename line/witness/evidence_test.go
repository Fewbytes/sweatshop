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
	info, err := os.Stat(filepath.Join(dir, res.EvidenceID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v", info.Mode().Perm())
	}
	dinfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
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

func TestTailBufferKeepsLastBytesInOrder(t *testing.T) {
	b := &tailBuffer{max: 10}
	for _, chunk := range []string{"abc", "defg", "hi", "jklmnopqrstuvwxyz1234", "5", "67"} {
		if _, err := b.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	// stream is ...v wxyz1234567; last 10 bytes
	if got, want := string(b.result()), "[truncated]\nxyz1234567"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
	small := &tailBuffer{max: 10}
	small.Write([]byte("hello"))
	if got := string(small.result()); got != "hello" {
		t.Fatalf("untruncated result = %q", got)
	}
}

func TestLogRunnerBoundsHugeOutput(t *testing.T) {
	dir := t.TempDir()
	res, err := LogRunner{Dir: dir}.Run(context.Background(), t.TempDir(), "head -c 6000000 /dev/zero | tr '\\0' x; echo END")
	if err != nil {
		t.Fatal(err)
	}
	data, err := ReadEvidence(dir, res.EvidenceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxEvidence+len("[truncated]\n") {
		t.Fatalf("log is %d bytes, over the cap", len(data))
	}
	if !bytes.HasPrefix(data, []byte("[truncated]\n")) || !bytes.HasSuffix(data, []byte("xxxEND\n")) {
		t.Fatal("tail not kept")
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
