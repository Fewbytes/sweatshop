package output

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
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
	reader := io.LimitReader(&repeatReader{pattern: []byte("0123456789abcde\n")}, size)
	result, err := Read(reader, Options{Lines: "1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != size {
		t.Fatalf("read %d bytes, want %d", result.Bytes, size)
	}
}

// boundedAllocLimit is the most a streaming Read/Preview may allocate in total
// while consuming a boundedAllocStreamSize stream for a small result.
const (
	boundedAllocStreamSize = 200 * 1024 * 1024
	boundedAllocLimit      = 64 * 1024 * 1024
)

// totalAllocDuring reports how many bytes the heap allocated while fn ran.
func totalAllocDuring(t *testing.T, fn func()) uint64 {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func largeStream() io.Reader {
	return io.LimitReader(&repeatReader{pattern: []byte("0123456789abcde\n")}, boundedAllocStreamSize)
}

const largeStreamLines = boundedAllocStreamSize / 16

func TestReadHeadRangeAllocationIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large stream fixture")
	}
	var result Result
	var err error
	allocated := totalAllocDuring(t, func() { result, err = Read(largeStream(), Options{Lines: "1:1"}) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "1:0123456789abcde" || result.Bytes != boundedAllocStreamSize || result.Lines != largeStreamLines {
		t.Fatalf("result = %+v", result)
	}
	t.Logf("allocated %d bytes", allocated)
	if allocated >= boundedAllocLimit {
		t.Fatalf("Read allocated %d bytes for a 1-line range of a %d-byte stream", allocated, boundedAllocStreamSize)
	}
}

func TestReadTailRangeAllocationIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large stream fixture")
	}
	var result Result
	var err error
	// An open-ended tail: the end is past the stream, so it is clamped to
	// the total line count only known once the stream is exhausted.
	spec := fmt.Sprintf("%d:%d", largeStreamLines-2, largeStreamLines+1000)
	allocated := totalAllocDuring(t, func() { result, err = Read(largeStream(), Options{Lines: spec}) })
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%d:0123456789abcde\n%d:0123456789abcde\n%d:0123456789abcde", largeStreamLines-2, largeStreamLines-1, largeStreamLines)
	if result.Text != want || result.Lines != largeStreamLines || result.Bytes != boundedAllocStreamSize {
		t.Fatalf("result = %+v", result)
	}
	t.Logf("allocated %d bytes", allocated)
	if allocated >= boundedAllocLimit {
		t.Fatalf("Read allocated %d bytes for a 3-line tail of a %d-byte stream", allocated, boundedAllocStreamSize)
	}
}

func TestReadGrepAllocationIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large stream fixture")
	}
	var result Result
	var err error
	allocated := totalAllocDuring(t, func() { result, err = Read(largeStream(), Options{Grep: "abc", Context: 2}) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Matches != largeStreamLines || result.Omitted != largeStreamLines-GrepMaxMatches || !result.Truncated {
		t.Fatalf("result metadata = %+v", result)
	}
	if got := strings.Count(result.Text, "\n") + 1; got != GrepMaxMatches+2 {
		t.Fatalf("grep shows %d lines, want %d", got, GrepMaxMatches+2)
	}
	t.Logf("allocated %d bytes", allocated)
	if allocated >= boundedAllocLimit {
		t.Fatalf("Read allocated %d bytes grepping a %d-byte stream", allocated, boundedAllocStreamSize)
	}
}

func TestPreviewAllocationIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large stream fixture")
	}
	var ref storage.StreamRef
	var err error
	allocated := totalAllocDuring(t, func() { ref, err = Preview(largeStream(), "inv_big", "stdout") })
	if err != nil {
		t.Fatal(err)
	}
	header := fmt.Sprintf("%d bytes, %d lines; showing 1-100 and %d-%d", boundedAllocStreamSize, largeStreamLines, largeStreamLines-99, largeStreamLines)
	if !ref.Truncated || ref.Bytes != boundedAllocStreamSize || ref.Lines != largeStreamLines || !strings.Contains(ref.Preview, header) {
		t.Fatalf("preview = %+v", ref)
	}
	t.Logf("allocated %d bytes", allocated)
	if allocated >= boundedAllocLimit {
		t.Fatalf("Preview allocated %d bytes for a %d-byte stream", allocated, boundedAllocStreamSize)
	}
}

// The ref* functions are the pre-streaming Read/Preview (io.ReadAll plus a
// bufio.Scanner split), kept verbatim as an oracle: the streaming versions
// must agree with them byte for byte on every input whose lines fit the old
// scanner's 16 MiB token limit.
func refSplitLines(data []byte) []line {
	if len(data) == 0 {
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var result []line
	number := 1
	for scanner.Scan() {
		copied := append([]byte(nil), scanner.Bytes()...)
		result = append(result, line{number, copied})
		number++
	}
	return result
}

func refPreview(reader io.Reader, invocationID, stream string) (storage.StreamRef, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return storage.StreamRef{}, err
	}
	lines := refSplitLines(data)
	ref := storage.StreamRef{Bytes: int64(len(data)), Lines: int64(len(lines))}
	if len(data) <= PreviewByteLimit && len(lines) <= PreviewHeadLines+PreviewTailLines {
		ref.Preview = string(data)
		return ref, nil
	}
	ref.Truncated = true
	headEnd := min(PreviewHeadLines, len(lines))
	tailStart := max(headEnd, len(lines)-PreviewTailLines)
	body := append([]line(nil), lines[:headEnd]...)
	body = append(body, lines[tailStart:]...)
	text := render(body, false)
	if len(text) > PreviewByteLimit {
		text = text[:PreviewByteLimit]
	}
	ref.Preview = fmt.Sprintf("[%s truncated — %d bytes, %d lines; showing 1-%d and %d-%d]\n%s\n→ BashOutput(id=\"%s\", stream=\"%s\")\n→ BashOutput(id=\"%s\", stream=\"%s\", lines=\"%d:%d\")",
		stream, len(data), len(lines), headEnd, tailStart+1, len(lines), text, invocationID, stream,
		invocationID, stream, headEnd+1, min(headEnd+200, len(lines)))
	return ref, nil
}

func refRead(reader io.Reader, options Options) (Result, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Result{}, err
	}
	all := refSplitLines(data)
	selected := all
	if options.Lines != "" {
		start, end, err := parseRange(options.Lines, len(all))
		if err != nil {
			return Result{}, err
		}
		selected = all[start-1 : end]
	}
	result := Result{Bytes: int64(len(data)), Lines: int64(len(all))}
	if options.Grep != "" {
		expression, err := regexp.Compile(options.Grep)
		if err != nil {
			return Result{}, fmt.Errorf("invalid grep expression: %w", err)
		}
		filtered, total, shown := grepLines(selected, expression, options.Context, GrepMaxMatches)
		selected = filtered
		result.Matches = int64(total)
		result.Omitted = int64(max(0, total-shown))
		result.Truncated = result.Omitted > 0
	}
	result.Text = render(selected, options.Lines != "" || options.Grep != "")
	return result, nil
}

func equivalenceInputs() map[string]string {
	inputs := map[string]string{
		"empty":            "",
		"newline":          "\n",
		"newlines":         "\n\n\n",
		"no-trailing":      "alpha\nbeta",
		"crlf":             "alpha\r\nbeta\r\n\r\ngamma\r",
		"lone-cr-eof":      "alpha\n\r",
		"cr-only":          "\r",
		"inner-cr":         "a\rb\nc\r\r\n",
		"blank-tail":       "x\n\n",
		"long-line":        strings.Repeat("L", 200*1024) + "\nshort\n" + strings.Repeat("M", 70*1024) + "\r\n",
		"long-unterminate": "a\n" + strings.Repeat("Z", 130*1024) + "\r",
		"preview-edge":     strings.Repeat("p", PreviewByteLimit),
		"preview-edge+1":   strings.Repeat("p", PreviewByteLimit+1),
		"preview-cr-edge":  strings.Repeat("q", PreviewByteLimit) + "\r\n" + strings.Repeat("r", PreviewByteLimit-1) + "\r\n",
	}
	var many, wide, errs strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&many, "line-%04d payload\n", i)
		fmt.Fprintf(&wide, "%s-%d\r\n", strings.Repeat("w", 9000), i)
		if i%7 == 0 {
			fmt.Fprintf(&errs, "error %d\n", i)
		} else {
			fmt.Fprintf(&errs, "ok %d\n", i)
		}
	}
	inputs["many"] = many.String()
	inputs["wide"] = wide.String()
	inputs["errors"] = errs.String()
	inputs["errors-no-trailing"] = strings.TrimSuffix(errs.String(), "\n")
	for _, n := range []int{100, 101, 199, 200, 201, 300} {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%d\n", i)
		}
		inputs[fmt.Sprintf("lines-%d", n)] = b.String()
	}
	return inputs
}

func TestStreamingReadMatchesReference(t *testing.T) {
	var optionSets []Options
	for _, lines := range []string{"", "1:1", "1:3", "2:4", "5:5", "3:2", "0:4", "x", "1:", "7:1000000", "950:1200", "1001:1001", "1000:1000", "100:200", "1:100000"} {
		for _, grep := range []string{"", "error", "^$", "p", "w-1", "(", "zzz", "."} {
			for _, context := range []int{0, 1, 3, -1, 1000} {
				if grep == "" && context != 0 {
					continue
				}
				optionSets = append(optionSets, Options{Lines: lines, Grep: grep, Context: context})
			}
		}
	}
	for name, input := range equivalenceInputs() {
		for _, options := range optionSets {
			want, wantErr := refRead(strings.NewReader(input), options)
			got, gotErr := Read(strings.NewReader(input), options)
			if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) {
				t.Fatalf("%s %+v: error = %v, want %v", name, options, gotErr, wantErr)
			}
			if got != want {
				t.Fatalf("%s %+v: result differs\n got: %.300q\nwant: %.300q\n(meta got %d/%d/%d/%d want %d/%d/%d/%d)", name, options,
					got.Text, want.Text, got.Bytes, got.Lines, got.Matches, got.Omitted, want.Bytes, want.Lines, want.Matches, want.Omitted)
			}
		}
	}
}

func TestStreamingPreviewMatchesReference(t *testing.T) {
	for name, input := range equivalenceInputs() {
		want, wantErr := refPreview(strings.NewReader(input), "inv_x", "stderr")
		got, gotErr := Preview(strings.NewReader(input), "inv_x", "stderr")
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) {
			t.Fatalf("%s: error = %v, want %v", name, gotErr, wantErr)
		}
		if got != want {
			t.Fatalf("%s: preview differs\n got: %+v\nwant: %+v", name, got, want)
		}
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestStreamingReadErrorTakesPrecedence(t *testing.T) {
	boom := errors.New("boom")
	for _, options := range []Options{{}, {Lines: "x"}, {Lines: "9:9"}, {Grep: "("}, {Lines: "1:2", Grep: "a"}} {
		_, err := Read(&failingReader{data: []byte("a\nb\n"), err: boom}, options)
		if !errors.Is(err, boom) {
			t.Fatalf("%+v: err = %v, want boom", options, err)
		}
	}
	if _, err := Preview(&failingReader{data: []byte("a\nb\n"), err: boom}, "inv", "stdout"); !errors.Is(err, boom) {
		t.Fatalf("preview err = %v, want boom", err)
	}
}

// repeatReader yields pattern forever without materialising the fixture, so
// the large-stream test measures Read's memory, not the test's.
type repeatReader struct {
	pattern []byte
	off     int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		c := copy(p[n:], r.pattern[r.off:])
		n += c
		r.off = (r.off + c) % len(r.pattern)
	}
	return n, nil
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
