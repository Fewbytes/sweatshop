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
	cmd := exec.Command("git", "diff", "--unified=0", base)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return ParseUnifiedDiff(out.String()), nil
}

var (
	newFileLine = regexp.MustCompile(`^\+\+\+ b/(.+)$`)
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
		if m := newFileLine.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if strings.HasPrefix(line, "+++ /dev/null") {
			current = ""
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
