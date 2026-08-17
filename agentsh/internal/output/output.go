package output

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
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
}

func Preview(reader io.Reader, invocationID, stream string) (storage.StreamRef, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return storage.StreamRef{}, err
	}
	lines := splitLines(data)
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

func Read(reader io.Reader, options Options) (Result, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Result{}, err
	}
	all := splitLines(data)
	selected := all
	if options.Lines != "" {
		start, end, err := parseRange(options.Lines, len(all))
		if err != nil {
			return Result{}, err
		}
		selected = all[start-1 : end]
	}
	if options.Grep != "" {
		expression, err := regexp.Compile(options.Grep)
		if err != nil {
			return Result{}, fmt.Errorf("invalid grep expression: %w", err)
		}
		included := map[int]bool{}
		for i, item := range selected {
			if expression.Match(item.data) {
				for j := max(0, i-options.Context); j <= min(len(selected)-1, i+options.Context); j++ {
					included[j] = true
				}
			}
		}
		filtered := make([]line, 0, len(included))
		indices := make([]int, 0, len(included))
		for i := range included {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		for _, i := range indices {
			filtered = append(filtered, selected[i])
		}
		selected = filtered
	}
	return Result{Text: render(selected, options.Lines != "" || options.Grep != ""), Bytes: int64(len(data)), Lines: int64(len(all))}, nil
}

type line struct {
	number int
	data   []byte
}

func splitLines(data []byte) []line {
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

func render(lines []line, numbered bool) string {
	var result strings.Builder
	for i, item := range lines {
		if numbered {
			fmt.Fprintf(&result, "%d:", item.number)
		}
		result.Write(item.data)
		if i < len(lines)-1 {
			result.WriteByte('\n')
		}
	}
	return result.String()
}

func parseRange(value string, total int) (int, int, error) {
	var start, end int
	if _, err := fmt.Sscanf(value, "%d:%d", &start, &end); err != nil || start < 1 || end < start {
		return 0, 0, errors.New("lines must be an inclusive A:B range with A >= 1")
	}
	if start > total {
		return 0, 0, fmt.Errorf("line range starts after end of output (%d lines)", total)
	}
	return start, min(end, total), nil
}
