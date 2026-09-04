package detector

import (
	"context"
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
