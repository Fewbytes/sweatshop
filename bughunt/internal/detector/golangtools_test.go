package detector

import (
	"context"
	"strings"
	"testing"

	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// staticcheck writes "file:line:col: message (SAxxxx)" to stdout.
func TestStaticcheckExtractsCheckIDFromSuffix(t *testing.T) {
	out := "/tmp/x/a.go:12:3: this value of err is never used (SA4006)\n"
	hits, err := NewStaticcheck(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].RuleID != "staticcheck/SA4006" {
		t.Fatalf("RuleID = %q, want staticcheck/SA4006", hits[0].RuleID)
	}
	if hits[0].Message != "this value of err is never used" {
		t.Fatalf("Message = %q, want the message without the check suffix", hits[0].Message)
	}
}

// The U family (unused code) is one of staticcheck's most common checks. Before
// it was added to the suffix pattern these findings collapsed to the generic id,
// so per-rule false-positive accounting could not distinguish them.
func TestStaticcheckExtractsUFamilyCheckID(t *testing.T) {
	out := "/tmp/x/a.go:8:6: func unusedHelper is unused (U1000)\n"
	hits, err := NewStaticcheck(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].RuleID != "staticcheck/U1000" {
		t.Fatalf("RuleID = %q, want staticcheck/U1000", hits[0].RuleID)
	}
	if hits[0].Message != "func unusedHelper is unused" {
		t.Fatalf("Message = %q, want the message without the check suffix", hits[0].Message)
	}
}

func TestStaticcheckWithoutCheckIDFallsBack(t *testing.T) {
	out := "/tmp/x/a.go:12:3: something happened\n"
	hits, err := NewStaticcheck(fakeRunner{stdout: out}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits[0].RuleID != "staticcheck/staticcheck" {
		t.Fatalf("RuleID = %q, want staticcheck/staticcheck", hits[0].RuleID)
	}
}

func TestErrcheckParsesStdout(t *testing.T) {
	out := "/tmp/x/a.go:9:10: defer f.Close()\n"
	hits, err := NewErrcheck(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].RuleID != "errcheck/unchecked" {
		t.Fatalf("hits = %+v, want one errcheck/unchecked", hits)
	}
}

// errcheck's real output separates the column from the message with a tab,
// not a space, e.g. "cmd/agentsh/main.go:508:17:\tdefer log.Close()".
func TestErrcheckParsesTabSeparatedStdout(t *testing.T) {
	out := "cmd/agentsh/main.go:508:17:\tdefer log.Close()\n"
	hits, err := NewErrcheck(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].File != "cmd/agentsh/main.go" {
		t.Fatalf("File = %q, want cmd/agentsh/main.go", hits[0].File)
	}
	if hits[0].Line != 508 {
		t.Fatalf("Line = %d, want 508", hits[0].Line)
	}
	if hits[0].RuleID != "errcheck/unchecked" {
		t.Fatalf("RuleID = %q, want errcheck/unchecked", hits[0].RuleID)
	}
	if strings.TrimSpace(hits[0].Message) != hits[0].Message {
		t.Fatalf("Message = %q, has leading/trailing whitespace", hits[0].Message)
	}
}

func TestIneffassignParsesStdout(t *testing.T) {
	out := "/tmp/x/a.go:4:2: ineffectual assignment to x\n"
	hits, err := NewIneffassign(fakeRunner{stdout: out, exitCode: 1}, symbol.NewGo()).
		Run(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].RuleID != "ineffassign/ineffassign" {
		t.Fatalf("hits = %+v, want one ineffassign/ineffassign", hits)
	}
}
