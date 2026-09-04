package detector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// fakeRunner replays captured tool output so parsing is tested without the tool.
type fakeRunner struct {
	stdout, stderr string
	exitCode       int
}

func (f fakeRunner) Run(context.Context, string, []string, string) (string, string, int, string, error) {
	return f.stdout, f.stderr, f.exitCode, "", nil
}

const vetSource = `package sample

import "fmt"

func Alpha(s string) {
	fmt.Printf("%d", s)
}
`

// go vet writes diagnostics to stderr as "file:line:col: message".
func TestGoVetParsesDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(vetSource), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := "# sample\n" + path + ":6:2: fmt.Printf format %d has arg s of wrong type string\n"

	d := NewGoVet(fakeRunner{stderr: stderr, exitCode: 1}, symbol.NewGo())
	hits, err := d.Run(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if h.RuleID != "govet/printf" {
		t.Errorf("RuleID = %q, want govet/printf", h.RuleID)
	}
	if h.Line != 6 {
		t.Errorf("Line = %d, want 6", h.Line)
	}
	if h.Symbol != "Alpha" {
		t.Errorf("Symbol = %q, want Alpha — hits must be anchored to a declaration", h.Symbol)
	}
	if h.MatchText != `fmt.Printf("%d", s)` {
		t.Errorf("MatchText = %q, want the flagged source line", h.MatchText)
	}
	if h.Severity != finding.SeverityMedium {
		t.Errorf("Severity = %q, want medium", h.Severity)
	}
}

func TestGoVetIgnoresNonDiagnosticLines(t *testing.T) {
	d := NewGoVet(fakeRunner{stderr: "# sample\nsome unrelated noise\n"}, symbol.NewGo())
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}

func TestGoVetCleanRunProducesNoHits(t *testing.T) {
	d := NewGoVet(fakeRunner{}, symbol.NewGo())
	hits, err := d.Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits, want 0", len(hits))
	}
}
