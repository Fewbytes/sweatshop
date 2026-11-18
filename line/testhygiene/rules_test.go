// Package testhygiene holds repo-wide static checks on the test code itself.
package testhygiene

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const analyzerAllowlistFile = "analyzer_allowlist.txt"

// proseCommentLines is the length of a // comment block inside a function body
// from which it counts as prose: a rationale that long belongs in the commit
// message, a doc comment or a named test, not between statements.
const proseCommentLines = 5

// analyzerRule is one static check over a _test.go file. count returns the
// number of violations; remedy is the failure message and names the honest
// alternative to the shortcut, with the guide that states the rule.
type analyzerRule struct {
	name   string
	remedy string
	count  func(path string, src []byte) int
}

func countMatchingLines(re *regexp.Regexp) func(string, []byte) int {
	return func(_ string, src []byte) int {
		n := 0
		for _, line := range strings.Split(string(src), "\n") {
			if re.MatchString(line) {
				n++
			}
		}
		return n
	}
}

// countProseComments counts // comment blocks of proseCommentLines or more
// lines that sit inside a function body.
func countProseComments(path string, src []byte) int {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return 0
	}
	n := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, group := range file.Comments {
			if group.Pos() < fn.Body.Lbrace || group.End() > fn.Body.Rbrace {
				continue
			}
			if strings.HasPrefix(group.List[0].Text, "//") && len(group.List) >= proseCommentLines {
				n++
			}
		}
	}
	return n
}

var analyzerRules = []analyzerRule{
	{
		name:   "wall-clock-wait",
		remedy: "time.Sleep or a shell \"sleep N\" in a test: use an injected clock, a channel the test waits on, or testwait.Until; for process tests, block on a pipe or file the test releases (testing guide, section 4)",
		count:  countMatchingLines(regexp.MustCompile(`time\.Sleep\(|\bsleep [0-9]`)),
	},
	{
		name:   "test-double-framework",
		remedy: "mocking framework in a test: write a small fake by hand at the port, local to the test package, and assert on outcomes, not on calls (testing guide, section 3)",
		count:  countMatchingLines(regexp.MustCompile(`"(github\.com/stretchr/testify/mock|github\.com/golang/mock/gomock|go\.uber\.org/mock/gomock|github\.com/vektra/mockery[^"]*)"`)),
	},
	{
		name:   "flake-masking",
		remedy: "a retry wrapper or a skip-on-flake hides a flaky test: find the unordered event or missing port that makes it flaky and make it a step the test controls (testing guide, section 4)",
		count:  countMatchingLines(regexp.MustCompile(`(?i)\bretry(Flaky|Test|Until)\w*\(|\brerun\w*\(|t\.Skip\w*\(.*flak`)),
	},
	{
		name:   "prose-comment",
		remedy: "a long comment block inside a function body: say why in the commit message or a doc comment, or rename and restructure so the code explains itself (writer guide, section 4)",
		count:  countProseComments,
	},
}

type allowKey struct{ rule, path string }

func readAnalyzerAllowlist(t *testing.T) map[allowKey]int {
	t.Helper()
	f, err := os.Open(analyzerAllowlistFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	allowed := map[allowKey]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%s: malformed line %q, want \"<rule> <path> <count>\"", analyzerAllowlistFile, line)
		}
		n, err := strconv.Atoi(fields[2])
		if err != nil || n < 1 {
			t.Fatalf("%s: bad count in %q", analyzerAllowlistFile, line)
		}
		allowed[allowKey{fields[0], fields[1]}] = n
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return allowed
}

// countViolations walks the repo's _test.go files and returns each rule's
// violation count per repo-relative path.
func countViolations(t *testing.T, root string, rules []analyzerRule) map[allowKey]int {
	t.Helper()
	counts := map[allowKey]int{}
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
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, r := range rules {
			if n := r.count(path, src); n > 0 {
				counts[allowKey{r.name, rel}] = n
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// checkAllowlist compares actual counts to the allowlist and returns one
// message per mismatch. The allowlist is shrink-only: a file under its count
// fails too, so the entry is lowered or deleted in the same change.
func checkAllowlist(rules []analyzerRule, actual, allowed map[allowKey]int) []string {
	remedy := map[string]string{}
	for _, r := range rules {
		remedy[r.name] = r.remedy
	}
	var problems []string
	for k, n := range actual {
		switch max, ok := allowed[k]; {
		case !ok:
			problems = append(problems, k.path+": "+strconv.Itoa(n)+" new "+k.rule+" violation(s): "+remedy[k.rule])
		case n > max:
			problems = append(problems, k.path+": "+strconv.Itoa(n)+" "+k.rule+" violations exceed the allowlist of "+strconv.Itoa(max)+": "+remedy[k.rule])
		case n < max:
			problems = append(problems, k.path+": only "+strconv.Itoa(n)+" "+k.rule+" violation(s) left; lower its "+analyzerAllowlistFile+" entry from "+strconv.Itoa(max)+" to "+strconv.Itoa(n))
		}
	}
	for k := range allowed {
		if _, ok := actual[k]; !ok {
			problems = append(problems, k.path+": no "+k.rule+" violations left; delete its "+analyzerAllowlistFile+" entry")
		}
	}
	sort.Strings(problems)
	return problems
}

// TestAnalyzerRules fails on a test-code shortcut named by analyzerRules
// beyond analyzer_allowlist.txt. Each failure message names the honest
// alternative. New rules come from the guides (line/guidance): add one to
// analyzerRules, run the test, and list the existing violations in the
// allowlist with their exact counts.
func TestAnalyzerRules(t *testing.T) {
	allowed := readAnalyzerAllowlist(t)
	actual := countViolations(t, filepath.Join("..", ".."), analyzerRules)
	for _, p := range checkAllowlist(analyzerRules, actual, allowed) {
		t.Error(p)
	}
}

func TestAllowlistIsShrinkOnly(t *testing.T) {
	rules := []analyzerRule{{name: "r", remedy: "use the honest alternative"}}
	allowed := map[allowKey]int{{"r", "a_test.go"}: 2, {"r", "gone_test.go"}: 1}
	actual := map[allowKey]int{{"r", "a_test.go"}: 1, {"r", "new_test.go"}: 1}
	got := strings.Join(checkAllowlist(rules, actual, allowed), "\n")
	for _, want := range []string{
		"a_test.go: only 1 r violation(s) left; lower its analyzer_allowlist.txt entry from 2 to 1",
		"gone_test.go: no r violations left; delete its analyzer_allowlist.txt entry",
		"new_test.go: 1 new r violation(s): use the honest alternative",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	actual = map[allowKey]int{{"r", "a_test.go"}: 3}
	if got := strings.Join(checkAllowlist(rules, actual, map[allowKey]int{{"r", "a_test.go"}: 2}), "\n"); !strings.Contains(got, "exceed the allowlist of 2: use the honest alternative") {
		t.Errorf("over-count not reported: %q", got)
	}
}

func TestProseCommentRuleCountsLongBlocksInFunctionBodies(t *testing.T) {
	src := "package p\n// doc\n// doc\n// doc\n// doc\n// doc\nfunc f() {\n\t// a\n\t// b\n\t// c\n\t// d\n\t// e\n\t_ = 1 // short\n}\n"
	if got := countProseComments("x_test.go", []byte(src)); got != 1 {
		t.Errorf("got %d prose comments, want 1 (the doc comment outside the body must not count)", got)
	}
}
