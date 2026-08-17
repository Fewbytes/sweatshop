package output

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

func TestPreviewTruncationHasExactMetadataAndRecovery(t *testing.T) {
	var input strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&input, "line-%04d payload payload payload\n", i)
	}
	ref, err := Preview(strings.NewReader(input.String()), "inv_deadbeef", "stdout")
	if err != nil {
		t.Fatal(err)
	}
	if !ref.Truncated || len(ref.Preview) > PreviewByteLimit+500 {
		t.Fatalf("unexpected preview bounds: %d %+v", len(ref.Preview), ref)
	}
	for _, want := range []string{"17000 bytes, 500 lines", "showing 1-100 and 401-500", `BashOutput(id="inv_deadbeef", stream="stdout")`} {
		if !strings.Contains(ref.Preview, want) {
			t.Fatalf("preview missing %q:\n%s", want, ref.Preview)
		}
	}
}

func TestReadLinesAndGrepContextAreNumbered(t *testing.T) {
	input := "alpha\nbeta\nerror here\ndelta\nepsilon\n"
	result, err := Read(strings.NewReader(input), Options{Grep: "error", Context: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "2:beta\n3:error here\n4:delta" {
		t.Fatalf("grep result = %q", result.Text)
	}
	result, err = Read(strings.NewReader(input), Options{Lines: "2:4"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "2:beta\n3:error here\n4:delta" {
		t.Fatalf("lines result = %q", result.Text)
	}
}

func TestLargeStreamIsReadWithoutCap(t *testing.T) {
	if testing.Short() {
		t.Skip("large stream fixture")
	}
	const size = 500 * 1024 * 1024
	reader := io.LimitReader(strings.NewReader(strings.Repeat("0123456789abcde\n", size/16)), size)
	result, err := Read(reader, Options{Lines: "1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != size {
		t.Fatalf("read %d bytes, want %d", result.Bytes, size)
	}
}

func TestReadFileIndexedMatchesRead(t *testing.T) {
	input := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	idx, err := storage.BuildLineIndex(strings.NewReader(input), storage.DefaultCheckpointLines, storage.DefaultCheckpointBytes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReadFile(bytes.NewReader([]byte(input)), idx, Options{Lines: "2:4"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "2:beta\n3:gamma\n4:delta" {
		t.Fatalf("indexed lines = %q", result.Text)
	}
	if result.Lines != 5 || result.Bytes != int64(len(input)) {
		t.Fatalf("metadata = %+v", result)
	}
}

func TestReadFileFallsBackWithoutIndex(t *testing.T) {
	input := "alpha\nbeta\ngamma\n"
	result, err := ReadFile(bytes.NewReader([]byte(input)), nil, Options{Lines: "2:3"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "2:beta\n3:gamma" {
		t.Fatalf("fallback lines = %q", result.Text)
	}
}

func TestReadFileGrepFallsBackToFullScan(t *testing.T) {
	input := "alpha\nerror here\ndelta\n"
	idx, err := storage.BuildLineIndex(strings.NewReader(input), storage.DefaultCheckpointLines, storage.DefaultCheckpointBytes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReadFile(bytes.NewReader([]byte(input)), idx, Options{Grep: "error", Context: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "1:alpha\n2:error here\n3:delta" {
		t.Fatalf("grep fallback = %q", result.Text)
	}
}

type countingReadSeeker struct {
	rs    io.ReadSeeker
	bytes int64
}

func (c *countingReadSeeker) Read(p []byte) (int, error) {
	n, err := c.rs.Read(p)
	c.bytes += int64(n)
	return n, err
}

func (c *countingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.rs.Seek(offset, whence)
}

func TestReadFileIndexedBoundsReadsOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-million-line fixture")
	}
	const total = 2_000_000
	path := filepath.Join(t.TempDir(), "blob")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(file, 1<<20)
	for i := 1; i <= total; i++ {
		if _, err := fmt.Fprintf(w, "line-%07d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	idxFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := storage.BuildLineIndex(idxFile, storage.DefaultCheckpointLines, storage.DefaultCheckpointBytes)
	idxFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	seekFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer seekFile.Close()
	counter := &countingReadSeeker{rs: seekFile}
	result, err := ReadFile(counter, idx, Options{Lines: "1999900:1999999"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Lines != total || result.Bytes != info.Size() {
		t.Fatalf("metadata = %+v", result)
	}
	if strings.Contains(result.Text, "1999900:") && !strings.Contains(result.Text, "2000000:") {
		// selected range near the end is present
	} else if !strings.Contains(result.Text, "1999900:") {
		t.Fatalf("range start missing from result: %q", result.Text[:40])
	}
	if counter.bytes >= info.Size()/100 {
		t.Fatalf("indexed read consumed %d bytes of %d for a 100-line range", counter.bytes, info.Size())
	}
}
