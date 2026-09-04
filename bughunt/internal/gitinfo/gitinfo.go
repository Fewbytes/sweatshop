// Package gitinfo reads the repository facts scan needs: the current commit and
// which lines a diff touched.
package gitinfo

import (
	"bufio"
	"bytes"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// HeadSHA returns the full SHA of HEAD.
func HeadSHA(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ChangedLines returns the lines added or modified relative to base, keyed by
// repo-relative path. --unified=0 keeps the hunks tight so unchanged context
// does not widen the gate.
func ChangedLines(dir, base string) (map[string][]int, error) {
	// Force deterministic prefixes regardless of the user's diff.mnemonicprefix,
	// diff.noprefix, or diff.dstPrefix config: any of those change what
	// "+++ " lines look like and would silently break newFileLine below.
	cmd := exec.Command("git",
		"-c", "diff.mnemonicPrefix=false",
		"-c", "diff.noprefix=false",
		"diff", "--no-ext-diff", "--unified=0", base)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return ParseUnifiedDiff(out.String()), nil
}

var (
	// The prefix is normally "b/" but diff.mnemonicprefix (i/, w/, c/, o/),
	// diff.noprefix, or diff.dstPrefix can change or remove it even though
	// ChangedLines forces the standard config; tolerate any single-letter
	// prefix (or none) rather than hard-coding "b/".
	newFileLine = regexp.MustCompile(`^\+\+\+ (?:[a-zA-Z]/)?(.+)$`)
	hunkHeader  = regexp.MustCompile(`^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)
)

// ParseUnifiedDiff extracts changed line numbers per file from unified diff
// text. Deleted files are skipped: they have no lines left to gate on.
func ParseUnifiedDiff(diff string) map[string][]int {
	changed := map[string][]int{}
	var current string

	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "+++ /dev/null") {
			current = ""
			continue
		}
		if m := newFileLine.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		m := hunkHeader.FindStringSubmatch(line)
		if m == nil || current == "" {
			continue
		}
		start, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		count := 1
		if m[2] != "" {
			count, err = strconv.Atoi(m[2])
			if err != nil {
				continue
			}
		}
		for i := 0; i < count; i++ {
			changed[current] = append(changed[current], start+i)
		}
	}
	return changed
}
