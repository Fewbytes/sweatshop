package detector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// writeTree writes files (slash path -> content) under a fresh temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// gitLog renders commits (each a file list) as `git log --name-only` output.
func gitLog(commits ...[]string) string {
	var b strings.Builder
	for _, files := range commits {
		b.WriteString("@@commit\n" + strings.Join(files, "\n") + "\n\n")
	}
	return b.String()
}

func repeatCommit(n int, files ...string) [][]string {
	out := make([][]string, n)
	for i := range out {
		out[i] = files
	}
	return out
}

const couplingGoMod = "module example.com/m\n\ngo 1.24\n"

var couplingTree = map[string]string{
	"go.mod":  couplingGoMod,
	"a/a.go":  "package a\n",
	"b/b.go":  "package b\nimport _ \"example.com/m/a\"\n",
	"c/c.go":  "package c\n",
	"c/c2.go": "package c\n",
	"d/d.go":  "package d\n",
}

func runCoupling(t *testing.T, log string) []string {
	t.Helper()
	root := writeTree(t, couplingTree)
	hits, err := NewHiddenCoupling(fakeRunner{stdout: log}, root).Run(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.MatchText)
	}
	return got
}

func TestHiddenCouplingReportsCochangeWithoutImportOrSharedPackage(t *testing.T) {
	log := gitLog(repeatCommit(couplingMinCochanges, "a/a.go", "d/d.go")...)
	got := runCoupling(t, log)
	if len(got) != 1 || got[0] != "a/a.go <-> d/d.go" {
		t.Fatalf("hits = %v, want the a/a.go <-> d/d.go pair", got)
	}
}

func TestHiddenCouplingIgnoresPairsWithStaticEdge(t *testing.T) {
	log := gitLog(append(repeatCommit(couplingMinCochanges, "a/a.go", "b/b.go"),
		repeatCommit(couplingMinCochanges, "c/c.go", "c/c2.go")...)...)
	if got := runCoupling(t, log); len(got) != 0 {
		t.Fatalf("import and same-package pairs are static, got hits %v", got)
	}
}

func TestHiddenCouplingNeedsMinimumCochanges(t *testing.T) {
	log := gitLog(repeatCommit(couplingMinCochanges-1, "a/a.go", "d/d.go")...)
	if got := runCoupling(t, log); len(got) != 0 {
		t.Fatalf("below threshold must not report, got %v", got)
	}
}

func TestHiddenCouplingSkipsBulkCommits(t *testing.T) {
	files := []string{"a/a.go", "d/d.go"}
	for i := 0; i < couplingMaxFilesPerCommit; i++ {
		files = append(files, "gone/"+string(rune('a'+i%26))+".txt")
	}
	log := gitLog(repeatCommit(couplingMinCochanges, files...)...)
	if got := runCoupling(t, log); len(got) != 0 {
		t.Fatalf("bulk commits must not count, got %v", got)
	}
}

func TestHiddenCouplingFailsOnGitError(t *testing.T) {
	root := writeTree(t, couplingTree)
	_, err := NewHiddenCoupling(fakeRunner{exitCode: 128, stderr: "not a git repository"}, root).Run(context.Background(), root, nil)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v, want git failure surfaced", err)
	}
}

const entropySource = `package p

import (
	"errors"
	"fmt"
	"log"
	"os"
)

func A() {
	log.Println("x")
	log.Println("y")
	fmt.Fprintln(os.Stderr, "z")
	_ = errors.New("a")
	_ = errors.New("b")
	_ = fmt.Errorf("c %d", 1)
	_ = fmt.Errorf("wrapped: %w", errors.New("d"))
}
`

func TestPatternEntropyReportsNonDominantIdiomsPerConcern(t *testing.T) {
	root := writeTree(t, map[string]string{"go.mod": couplingGoMod, "p/p.go": entropySource})
	hits, err := NewPatternEntropy(symbol.NewGo()).Run(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := map[string]string{}
	for _, h := range hits {
		got[h.RuleID] = h.MatchText
		if h.Symbol != "A" {
			t.Errorf("%s Symbol = %q, want A", h.RuleID, h.Symbol)
		}
	}
	want := map[string]string{
		"patternentropy/logging":    "logging/fmt.Fprint*(os.Stderr)",
		"patternentropy/leaf-error": "leaf-error/fmt.Errorf",
	}
	if len(got) != len(want) {
		t.Fatalf("hits = %v, want %v", got, want)
	}
	for rule, match := range want {
		if got[rule] != match {
			t.Errorf("%s MatchText = %q, want %q", rule, got[rule], match)
		}
	}
}

func TestPatternEntropyIgnoresTestFilesAndSingleIdiomConcerns(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":      couplingGoMod,
		"p/p.go":      "package p\nimport \"errors\"\nvar E = errors.New(\"x\")\n",
		"p/p_test.go": "package p\nimport \"fmt\"\nvar F = fmt.Errorf(\"y\")\n",
	})
	hits, err := NewPatternEntropy(symbol.NewGo()).Run(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %v, want none", hits)
	}
}

func bigFunc(name string) string {
	return "func " + name + "() {\n" + strings.Repeat("\t_ = \"padding padding padding padding\"\n", 500) + "}\n"
}

func TestContextClosureCountsTransitiveDefinitionsOnce(t *testing.T) {
	src := "package p\n\n" +
		"func Entry() { Mid(); Mid() }\n" +
		"func Mid() { Heavy() }\n" +
		bigFunc("Heavy") +
		"func Small() {}\n"
	root := writeTree(t, map[string]string{"go.mod": couplingGoMod, "p/p.go": src})
	hits, err := NewContextClosure(symbol.NewGo()).Run(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := map[string]bool{}
	for _, h := range hits {
		got[h.MatchText] = true
	}
	for _, name := range []string{"Entry", "Mid", "Heavy"} {
		if !got[name] {
			t.Errorf("%s should exceed the closure limit, hits = %v", name, got)
		}
	}
	if got["Small"] || len(got) != 3 {
		t.Errorf("only the three heavy chains should be reported, got %v", got)
	}
}

func TestContextClosureDoesNotDoubleCountRecursion(t *testing.T) {
	// ~12 KB is 3000 tokens: under the limit alone, over it if counted twice.
	body := "func R() {\n\tR()\n" + strings.Repeat("\t_ = \"padding padding padding\"\n", 400) + "}\n"
	root := writeTree(t, map[string]string{"go.mod": couplingGoMod, "p/p.go": "package p\n" + body})
	hits, err := NewContextClosure(symbol.NewGo()).Run(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("recursive function counted twice: %v", hits)
	}
}

func TestContextClosureIgnoresMembersNamedLikePackageSymbols(t *testing.T) {
	src := "package p\n\nfunc Entry(x struct{ Heavy int }) int { return x.Heavy }\n" + bigFunc("Heavy")
	root := writeTree(t, map[string]string{"go.mod": couplingGoMod, "p/p.go": src})
	hits, err := NewContextClosure(symbol.NewGo()).Run(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, h := range hits {
		if h.MatchText == "Entry" {
			t.Fatalf("x.Heavy is a field, not a reference to func Heavy: %v", h)
		}
	}
}

func TestHiddenCouplingUnavailableOutsideGitRepository(t *testing.T) {
	if NewHiddenCoupling(fakeRunner{}, t.TempDir()).Available(context.Background()) {
		t.Fatal("a directory outside any repository has no history to measure")
	}
}
