package output

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

func tempFile(t *testing.T, content string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "blob")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestGrepLinesBoundedAndNumbered(t *testing.T) {
	input := "alpha\nerror one\ngamma\nerror two\ndelta\n"
	idx, err := storage.BuildLineIndex(strings.NewReader(input), storage.DefaultCheckpointLines, storage.DefaultCheckpointBytes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReadFile(bytes.NewReader([]byte(input)), idx, Options{Grep: "error", Context: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches != 2 || result.Omitted != 0 || result.Truncated {
		t.Fatalf("metadata = %+v", result)
	}
	if result.Text != "2:error one\n4:error two" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestGrepLinesBounded(t *testing.T) {
	lines := make([]line, 0, 10)
	for i := 1; i <= 10; i++ {
		data := fmt.Sprintf("row %d", i)
		if i%2 == 0 {
			data = "hit " + data
		}
		lines = append(lines, line{number: i, data: []byte(data)})
	}
	expr := regexp.MustCompile("hit")
	selected, total, shown := grepLines(lines, expr, 1, 2)
	if total != 5 || shown != 2 {
		t.Fatalf("total=%d shown=%d", total, shown)
	}
	if len(selected) != 5 {
		// lines 2(1..3) + 4(3..5) → 1,2,3,4,5
		t.Fatalf("selected len = %d, want 5", len(selected))
	}
}

func TestGrepFallbackWithoutRg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	file := tempFile(t, "a\nb\nerror one\nd\nerror two\ne\n")
	result, err := Grep(file, Options{Grep: "error", Context: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches != 2 || result.Omitted != 0 || result.Truncated {
		t.Fatalf("metadata = %+v", result)
	}
	if result.Text != "3:error one\n5:error two" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	file := tempFile(t, "hello\n")
	_, err := Grep(file, Options{Grep: "[invalid"})
	if err == nil || !strings.Contains(err.Error(), "invalid grep expression") {
		t.Fatalf("expected invalid regex error, got %v", err)
	}
}

func TestGrepNoMatches(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	file := tempFile(t, "alpha\nbeta\n")
	result, err := Grep(file, Options{Grep: "zzz"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches != 0 || result.Text != "" || result.Truncated {
		t.Fatalf("no-match result = %+v", result)
	}
}

func TestGrepBinaryLikeData(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	file := tempFile(t, "line\x001\n\x00error\x00\nplain\n")
	result, err := Grep(file, Options{Grep: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches != 1 {
		t.Fatalf("binary-ish grep result = %+v", result)
	}
	if !strings.Contains(result.Text, "2:") {
		t.Fatalf("binary-ish grep text missing line number: %q", result.Text)
	}
}

func TestGrepBoundedOnHugeLog(t *testing.T) {
	if testing.Short() {
		t.Skip("huge log fixture")
	}
	t.Setenv("PATH", t.TempDir())
	f, err := os.CreateTemp(t.TempDir(), "blob")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	w := bufio.NewWriterSize(f, 1<<20)
	for i := 1; i <= 200_000; i++ {
		if _, err := fmt.Fprintf(w, "hit line %d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	result, err := Grep(f, Options{Grep: "hit"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches != 200_000 || result.Omitted != 200_000-GrepMaxMatches {
		t.Fatalf("bounded metadata = %+v", result)
	}
	if !result.Truncated {
		t.Fatal("expected Truncated=true")
	}
	lines := strings.Count(result.Text, "\n") + 1
	if result.Text == "" {
		lines = 0
	}
	if lines > GrepMaxMatches+2 {
		t.Fatalf("text not bounded: %d lines", lines)
	}
}

func TestGrepWithRgWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	file := tempFile(t, "alpha\nbeta\nerror one\ngamma\nerror two\ndelta\nerror three\n")
	result, err := Grep(file, Options{Grep: "error", Context: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches != 3 {
		t.Fatalf("rg grep matches = %d, want 3", result.Matches)
	}
	if !strings.Contains(result.Text, "3:error one") || !strings.Contains(result.Text, "5:error two") || !strings.Contains(result.Text, "7:error three") {
		t.Fatalf("rg grep text = %q", result.Text)
	}
	if result.Truncated {
		t.Fatal("expected not truncated")
	}
}
