package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const diff = `diff --git a/internal/scan/scan.go b/internal/scan/scan.go
index 1111111..2222222 100644
--- a/internal/scan/scan.go
+++ b/internal/scan/scan.go
@@ -10,0 +11,2 @@ func Run() {
+	added := 1
+	_ = added
@@ -40 +42 @@ func Other() {
-	old()
+	replaced()
diff --git a/deleted.go b/deleted.go
deleted file mode 100644
--- a/deleted.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package x
-
-func Gone() {}
`

func TestParseUnifiedDiff(t *testing.T) {
	got := ParseUnifiedDiff(diff)
	want := map[string][]int{"internal/scan/scan.go": {11, 12, 42}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseUnifiedDiff = %v, want %v", got, want)
	}
}

func TestParseUnifiedDiffIgnoresDeletedFiles(t *testing.T) {
	got := ParseUnifiedDiff(diff)
	if _, ok := got["deleted.go"]; ok {
		t.Fatal("a deleted file has no changed lines to gate on")
	}
}

func TestParseUnifiedDiffEmptyInput(t *testing.T) {
	if got := ParseUnifiedDiff(""); len(got) != 0 {
		t.Fatalf("ParseUnifiedDiff(\"\") = %v, want empty", got)
	}
}

// TestChangedLinesWithMnemonicPrefix reproduces the real bug: a user with
// diff.mnemonicprefix = true (or diff.noprefix, or diff.dstPrefix) in their
// global gitconfig gets "+++ w/path" (or other prefixes) instead of
// "+++ b/path" out of plain `git diff`. ChangedLines must force deterministic
// prefixes on the git invocation itself, so it must return the changed lines
// correctly even with that config active in the environment. This shells out
// to a real temp git repo rather than a hand-written fixture, since a
// hand-written fixture is exactly what let the original bug through.
func TestChangedLinesWithMnemonicPrefix(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-q")
	// Simulate the author's own machine: diff.mnemonicprefix = true makes
	// plain `git diff` emit "+++ w/path" instead of "+++ b/path".
	run("config", "diff.mnemonicprefix", "true")
	run("config", "user.name", "test")
	run("config", "user.email", "test@example.com")
	run("config", "commit.gpgsign", "false")
	run("config", "tag.gpgsign", "false")

	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "initial")

	if err := os.WriteFile(file, []byte("package main\n\nfunc main() {\n\tprintln(1)\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "main.go")
	run("commit", "-q", "-m", "add a line")

	// Confirm the environment actually reproduces the bug precondition.
	raw := exec.Command("git", "diff", "--unified=0", "HEAD~1")
	raw.Dir = dir
	out, err := raw.Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	if !strings.Contains(string(out), "+++ w/") {
		t.Skipf("this git does not reproduce mnemonicprefix output (%q); skipping", out)
	}

	got, err := ChangedLines(dir, "HEAD~1")
	if err != nil {
		t.Fatalf("ChangedLines: %v", err)
	}
	if lines, ok := got["main.go"]; !ok || len(lines) == 0 {
		t.Fatalf("ChangedLines = %v, want non-empty entry for main.go", got)
	}
}
