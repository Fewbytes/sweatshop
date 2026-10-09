package output

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Fewbytes/sweatshop/agentsh/internal/storage"
)

const (
	PreviewHeadLines = 100
	PreviewTailLines = 100
	PreviewByteLimit = 8 * 1024
)

type Options struct {
	Lines   string
	Grep    string
	Context int
}

type Result struct {
	Text      string `json:"text"`
	Bytes     int64  `json:"bytes"`
	Lines     int64  `json:"lines"`
	Truncated bool   `json:"truncated"`
	Running   bool   `json:"running,omitempty"`
	Matches   int64  `json:"matches,omitempty"`
	Omitted   int64  `json:"omitted,omitempty"`
}

// Preview summarises a stream in one pass. Memory is bounded independently of
// the stream: the raw first PreviewByteLimit bytes, the first PreviewHeadLines
// lines and a ring of the last PreviewTailLines lines, each line truncated to
// PreviewByteLimit bytes (the rendered body is cut to that length anyway, so
// no line contributes more than that to the result).
func Preview(reader io.Reader, invocationID, stream string) (storage.StreamRef, error) {
	prefix := &prefixRecorder{r: reader, limit: PreviewByteLimit}
	lines := newLineReader(prefix)
	head := make([]line, 0, PreviewHeadLines)
	tail := lineRing{limit: PreviewTailLines}
	for {
		number := lines.count + 1
		data, ok, err := lines.next(PreviewByteLimit)
		if err != nil {
			return storage.StreamRef{}, err
		}
		if !ok {
			break
		}
		if number <= PreviewHeadLines {
			head = append(head, line{number: number, data: bytes.Clone(data)})
		} else {
			tail.push(number, data)
		}
	}
	total, size := lines.count, lines.bytes
	ref := storage.StreamRef{Bytes: size, Lines: int64(total)}
	if size <= PreviewByteLimit && total <= PreviewHeadLines+PreviewTailLines {
		ref.Preview = string(prefix.data)
		return ref, nil
	}
	ref.Truncated = true
	// The ring holds exactly lines max(headEnd, total-PreviewTailLines)+1..total.
	headEnd := min(PreviewHeadLines, total)
	tailStart := max(headEnd, total-PreviewTailLines)
	body := renderer{}
	for _, item := range head {
		body.write(item.number, item.data)
	}
	tail.each(func(number int, data []byte) { body.write(number, data) })
	text := body.String()
	if len(text) > PreviewByteLimit {
		text = text[:PreviewByteLimit]
	}
	ref.Preview = fmt.Sprintf("[%s truncated — %d bytes, %d lines; showing 1-%d and %d-%d]\n%s\n→ BashOutput(id=\"%s\", stream=\"%s\")\n→ BashOutput(id=\"%s\", stream=\"%s\", lines=\"%d:%d\")",
		stream, size, total, headEnd, tailStart+1, total, text, invocationID, stream,
		invocationID, stream, headEnd+1, min(headEnd+200, total))
	return ref, nil
}

// Read renders a stream's output in one pass. Memory is bounded by the result,
// not the stream: lines outside the requested range are counted and skipped
// without being buffered, and a grep keeps only its context window (at most
// Context lines) beyond what it returns. A read without a range or grep
// returns the whole stream, so that path is bounded by the stream itself.
func Read(reader io.Reader, options Options) (Result, error) {
	var expression *regexp.Regexp
	var grepErr error
	if options.Grep != "" {
		if expression, grepErr = regexp.Compile(options.Grep); grepErr != nil {
			grepErr = fmt.Errorf("invalid grep expression: %w", grepErr)
		}
	}
	return readStream(reader, options.Lines, expression, grepErr, options.Context)
}

// readStream is Read with the grep expression already compiled. Errors are
// reported in the order the whole-buffer implementation reported them: read
// errors first, then the line range, then the grep expression.
func readStream(reader io.Reader, lineRange string, expression *regexp.Regexp, grepErr error, context int) (Result, error) {
	start, end := 1, math.MaxInt
	var rangeErr error
	if lineRange != "" {
		start, end, rangeErr = parseRangeSpec(lineRange)
	}
	collect := rangeErr == nil && grepErr == nil
	out := &renderer{numbered: lineRange != "" || expression != nil || grepErr != nil}
	var grep *streamGrep
	if expression != nil {
		grep = &streamGrep{expression: expression, context: context, maxMatches: GrepMaxMatches, out: out, afterUntil: -1, before: lineRing{limit: context}}
	}
	lines := newLineReader(reader)
	for {
		number := lines.count + 1
		selected := collect && number >= start && number <= end
		keep := 0
		if selected {
			keep = -1
		}
		data, ok, err := lines.next(keep)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			break
		}
		if !selected {
			continue
		}
		if grep != nil {
			grep.add(number, data)
		} else {
			out.write(number, data)
		}
	}
	total := lines.count
	if lineRange != "" {
		if _, _, err := parseRange(lineRange, total); err != nil {
			return Result{}, err
		}
	}
	if grepErr != nil {
		return Result{}, grepErr
	}
	result := Result{Bytes: lines.bytes, Lines: int64(total)}
	if grep != nil {
		result.Matches = int64(grep.total)
		result.Omitted = int64(max(0, grep.total-grep.shown))
		result.Truncated = result.Omitted > 0
	}
	result.Text = out.String()
	return result, nil
}

// ReadFile reads output from a seekable stream, using an optional line index
// to bound the read when a line range is requested without grep. When the
// index is unavailable or a grep is requested, it falls back to scanning the
// whole stream.
func ReadFile(reader io.ReadSeeker, idx *storage.LineIndex, options Options) (Result, error) {
	if options.Grep != "" {
		if options.Lines == "" {
			if file, ok := reader.(*os.File); ok {
				return Grep(file, options)
			}
		}
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return Result{}, err
		}
		return Read(reader, options)
	}
	if options.Lines == "" || idx == nil || len(idx.Entries) == 0 {
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return Result{}, err
		}
		return Read(reader, options)
	}
	start, end, err := parseRange(options.Lines, int(idx.Lines))
	if err != nil {
		return Result{}, err
	}
	selected, err := readLineRange(reader, int64(start), int64(end), idx.Lookup(int64(start)))
	if err != nil {
		return Result{}, err
	}
	return Result{Text: render(selected, true), Bytes: idx.Bytes, Lines: idx.Lines}, nil
}

func readLineRange(r io.ReadSeeker, start, end int64, entry storage.IndexEntry) ([]line, error) {
	if _, err := r.Seek(entry.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(r, 64*1024)
	for skip := start - entry.Line; skip > 0; skip-- {
		if _, err := br.ReadBytes('\n'); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
	}
	result := make([]line, 0, int(end-start+1))
	for number := start; number <= end; number++ {
		b, err := br.ReadBytes('\n')
		if len(b) > 0 {
			result = append(result, line{number: int(number), data: stripLineEnding(b)})
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	return result, nil
}

func stripLineEnding(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
		if len(b) > 0 && b[len(b)-1] == '\r' {
			b = b[:len(b)-1]
		}
	}
	return b
}

type line struct {
	number int
	data   []byte
}

// lineReader splits a stream into lines exactly as bufio.ScanLines does (split
// on '\n', drop one trailing '\r', emit a final unterminated line when it is
// non-empty), but with no line-length limit: a line longer than the read
// buffer is consumed in chunks and only as much of it as the caller asks to
// keep is buffered.
type lineReader struct {
	r     *bufio.Reader
	buf   []byte
	bytes int64
	count int
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 64*1024)}
}

// next returns the next line's content, keeping at most keep bytes of it
// (keep < 0 keeps the whole line). The slice is only valid until the next
// call. ok is false once the stream is exhausted.
func (l *lineReader) next(keep int) (data []byte, ok bool, err error) {
	l.buf = l.buf[:0]
	var raw int
	var last, beforeLast byte
	for {
		chunk, readErr := l.r.ReadSlice('\n')
		raw += len(chunk)
		l.bytes += int64(len(chunk))
		if keep < 0 {
			l.buf = append(l.buf, chunk...)
		} else if room := keep - len(l.buf); room > 0 {
			l.buf = append(l.buf, chunk[:min(room, len(chunk))]...)
		}
		switch n := len(chunk); {
		case n >= 2:
			beforeLast, last = chunk[n-2], chunk[n-1]
		case n == 1:
			beforeLast, last = last, chunk[0]
		}
		if readErr == nil {
			break
		}
		if readErr == bufio.ErrBufferFull {
			continue
		}
		if readErr == io.EOF {
			if raw == 0 {
				return nil, false, nil
			}
			break
		}
		return nil, false, readErr
	}
	content := raw
	if last == '\n' {
		content--
		if content > 0 && beforeLast == '\r' {
			content--
		}
	} else if last == '\r' {
		content--
	}
	l.count++
	return l.buf[:min(len(l.buf), content)], true, nil
}

// prefixRecorder passes reads through, retaining the first limit bytes.
type prefixRecorder struct {
	r     io.Reader
	limit int
	data  []byte
}

func (p *prefixRecorder) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if room := p.limit - len(p.data); room > 0 && n > 0 {
		p.data = append(p.data, b[:min(room, n)]...)
	}
	return n, err
}

// lineRing keeps copies of the most recent limit lines, reusing its buffers.
type lineRing struct {
	slots []line
	start int
	count int
	limit int
}

func (r *lineRing) push(number int, data []byte) {
	if r.limit <= 0 {
		return
	}
	var slot *line
	if r.count < r.limit {
		// Not yet full, so start is 0 and slots fill in order.
		if r.count == len(r.slots) {
			r.slots = append(r.slots, line{})
		}
		slot = &r.slots[r.count]
		r.count++
	} else {
		slot = &r.slots[r.start]
		r.start = (r.start + 1) % len(r.slots)
	}
	slot.number = number
	slot.data = append(slot.data[:0], data...)
}

// each visits the retained lines oldest first.
func (r *lineRing) each(fn func(number int, data []byte)) {
	for k := 0; k < r.count; k++ {
		item := r.slots[(r.start+k)%len(r.slots)]
		fn(item.number, item.data)
	}
}

func (r *lineRing) reset() {
	r.start, r.count = 0, 0
}

// streamGrep is grepLines over a stream: it selects the same lines (the first
// maxMatches matches plus context, clipped to the input) and counts every
// match, holding at most context unemitted lines for before-context.
type streamGrep struct {
	expression *regexp.Regexp
	context    int
	maxMatches int
	out        *renderer
	before     lineRing
	index      int
	afterUntil int
	total      int
	shown      int
}

func (g *streamGrep) add(number int, data []byte) {
	i := g.index
	g.index++
	matched := g.expression.Match(data)
	if matched {
		g.total++
	}
	limited := g.maxMatches > 0 && g.shown >= g.maxMatches
	if matched && !limited {
		g.shown++
		if g.context < 0 {
			// grepLines' window [m-context, m+context] is empty.
			return
		}
		// The ring holds the unemitted lines immediately preceding this
		// one, at most context of them: exactly its before-context.
		g.before.each(g.out.write)
		g.before.reset()
		g.out.write(number, data)
		g.afterUntil = i + g.context
		return
	}
	if i <= g.afterUntil {
		g.out.write(number, data)
		return
	}
	if !limited {
		g.before.push(number, data)
	}
}

// renderer joins lines with '\n', optionally prefixing each with "N:".
type renderer struct {
	text     strings.Builder
	numbered bool
	written  bool
}

func (r *renderer) write(number int, data []byte) {
	if r.written {
		r.text.WriteByte('\n')
	}
	r.written = true
	if r.numbered {
		r.text.WriteString(strconv.Itoa(number))
		r.text.WriteByte(':')
	}
	r.text.Write(data)
}

func (r *renderer) String() string {
	return r.text.String()
}

func render(lines []line, numbered bool) string {
	result := renderer{numbered: numbered}
	for _, item := range lines {
		result.write(item.number, item.data)
	}
	return result.String()
}

// parseRangeSpec validates the syntax of an inclusive "A:B" line range.
func parseRangeSpec(value string) (int, int, error) {
	var start, end int
	if _, err := fmt.Sscanf(value, "%d:%d", &start, &end); err != nil || start < 1 || end < start {
		return 0, 0, errors.New("lines must be an inclusive A:B range with A >= 1")
	}
	return start, end, nil
}

func parseRange(value string, total int) (int, int, error) {
	start, end, err := parseRangeSpec(value)
	if err != nil {
		return 0, 0, err
	}
	if start > total {
		return 0, 0, fmt.Errorf("line range starts after end of output (%d lines)", total)
	}
	return start, min(end, total), nil
}
