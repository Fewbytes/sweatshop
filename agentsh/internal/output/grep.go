package output

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GrepMaxMatches bounds the number of matching lines a grep returns. Matches
// beyond this count are reported through Result.Omitted and Result.Truncated.
const GrepMaxMatches = 100

// Grep searches a stream file for a regex, returning bounded line-numbered
// matches with context and explicit match/omission metadata. It uses ripgrep
// when available and falls back to the built-in regexp engine otherwise.
func Grep(file *os.File, options Options) (Result, error) {
	expression, err := regexp.Compile(options.Grep)
	if err != nil {
		return Result{}, fmt.Errorf("invalid grep expression: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return Result{}, err
	}
	if rgPath, lookupErr := exec.LookPath("rg"); lookupErr == nil {
		if result, ok := grepWithRg(rgPath, file.Name(), expression, options.Context); ok {
			result.Bytes = info.Size()
			return result, nil
		}
	}
	return grepWithGo(file, expression, options.Context, info.Size())
}

// grepWithRg runs ripgrep over the blob. ok=false means the caller should fall
// back to the pure-Go path (rg unavailable, crashed, or rejected the pattern).
func grepWithRg(rgPath, filePath string, expression *regexp.Regexp, context int) (Result, bool) {
	pattern := expression.String()

	total := 0
	out, code, err := runRg(rgPath, "--count", "--text", "-e", pattern, "--", filePath)
	if err != nil {
		return Result{}, false
	}
	switch code {
	case 0:
		total, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	case 2:
		return Result{}, false
	}

	args := []string{"--color", "never", "--text", "-n"}
	if context > 0 {
		args = append(args, "-C", strconv.Itoa(context))
	}
	args = append(args, "-m", strconv.Itoa(GrepMaxMatches), "-e", pattern, "--", filePath)
	out, code, err = runRg(rgPath, args...)
	if err != nil || code == 2 {
		return Result{}, false
	}

	shown := total
	if shown > GrepMaxMatches {
		shown = GrepMaxMatches
	}
	omitted := total - shown
	return Result{
		Text:      string(out),
		Matches:   int64(total),
		Omitted:   int64(omitted),
		Truncated: omitted > 0,
	}, true
}

func grepWithGo(file *os.File, expression *regexp.Regexp, context int, size int64) (Result, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return Result{}, err
	}
	all := splitLines(data)
	selected, total, shown := grepLines(all, expression, context, GrepMaxMatches)
	omitted := total - shown
	return Result{
		Text:      render(selected, true),
		Bytes:     int64(len(data)),
		Lines:     int64(len(all)),
		Matches:   int64(total),
		Omitted:   int64(max(0, omitted)),
		Truncated: omitted > 0,
	}, nil
}

// grepLines returns the lines to show for a grep (matches plus context within
// the given slice) together with the total and shown match counts.
func grepLines(lines []line, expression *regexp.Regexp, context, maxMatches int) ([]line, int, int) {
	matches := make([]int, 0)
	for i := range lines {
		if expression.Match(lines[i].data) {
			matches = append(matches, i)
		}
	}
	total := len(matches)
	shown := matches
	if maxMatches > 0 && len(shown) > maxMatches {
		shown = shown[:maxMatches]
	}
	included := make(map[int]bool, len(shown)*(2*context+1))
	for _, m := range shown {
		lo := m - context
		if lo < 0 {
			lo = 0
		}
		hi := m + context
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for j := lo; j <= hi; j++ {
			included[j] = true
		}
	}
	indices := make([]int, 0, len(included))
	for i := range included {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	selected := make([]line, 0, len(indices))
	for _, i := range indices {
		selected = append(selected, lines[i])
	}
	return selected, total, len(shown)
}

func runRg(rgPath string, args ...string) ([]byte, int, error) {
	cmd := exec.Command(rgPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.Bytes(), exitErr.ExitCode(), nil
		}
		return stdout.Bytes(), -1, err
	}
	return stdout.Bytes(), 0, nil
}
