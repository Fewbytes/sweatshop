// Package testhygiene holds repo-wide checks on the test code itself.
package testhygiene

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const sleepAllowlistFile = "sleep_allowlist.txt"

var wallClockWait = regexp.MustCompile(`time\.Sleep\(|\bsleep [0-9]`)

func readSleepAllowlist(t *testing.T) map[string]int {
	t.Helper()
	f, err := os.Open(sleepAllowlistFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allowed := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("%s: malformed line %q, want \"<path> <count>\"", sleepAllowlistFile, line)
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 1 {
			t.Fatalf("%s: bad count in %q", sleepAllowlistFile, line)
		}
		allowed[fields[0]] = n
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return allowed
}

func countSleepsInTests(t *testing.T, root string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n := 0
		for _, line := range strings.Split(string(data), "\n") {
			if wallClockWait.MatchString(line) {
				n++
			}
		}
		if n > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			counts[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// TestNoNewSleepsInTests fails on a wall-clock wait (time.Sleep or a shell
// "sleep N") in a _test.go file beyond sleep_allowlist.txt. The allowlist is
// shrink-only: it also fails when a file is under its count, so the entry must
// be lowered or deleted in the same change. Replace sleeps with an injected
// clock, a channel the test waits on, or testwait.Until; for process tests,
// block on a pipe or file the test releases.
func TestNoNewSleepsInTests(t *testing.T) {
	allowed := readSleepAllowlist(t)
	actual := countSleepsInTests(t, filepath.Join("..", ".."))
	for file, n := range actual {
		switch max, ok := allowed[file]; {
		case !ok:
			t.Errorf("%s: %d new sleep(s) in tests; use an injected clock, channel, or testwait.Until", file, n)
		case n > max:
			t.Errorf("%s: %d sleeps exceeds allowlist of %d", file, n, max)
		case n < max:
			t.Errorf("%s: only %d sleeps left; lower its %s entry from %d to %d", file, n, sleepAllowlistFile, max, n)
		}
	}
	for file := range allowed {
		if _, ok := actual[file]; !ok {
			t.Errorf("%s: no sleeps left; delete its %s entry", file, sleepAllowlistFile)
		}
	}
}
