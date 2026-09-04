package symbol

import (
	"os"
	"path/filepath"
	"testing"
)

const src = `package sample

import "fmt"

var topLevel = 1

func Alpha() {
	fmt.Println("a")
}

type T struct{}

func (t *T) Beta() {
	fmt.Println("b")
}
`

func writeSample(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnclosingFunction(t *testing.T) {
	p := writeSample(t)
	r := NewGo()
	got, ok := r.Enclosing(p, 8) // fmt.Println("a")
	if !ok || got != "Alpha" {
		t.Fatalf("Enclosing = %q, %v; want \"Alpha\", true", got, ok)
	}
}

func TestEnclosingMethodIncludesReceiver(t *testing.T) {
	p := writeSample(t)
	got, ok := NewGo().Enclosing(p, 14) // fmt.Println("b")
	if !ok || got != "T.Beta" {
		t.Fatalf("Enclosing = %q, %v; want \"T.Beta\", true", got, ok)
	}
}

func TestEnclosingPackageLevelVar(t *testing.T) {
	p := writeSample(t)
	got, ok := NewGo().Enclosing(p, 5)
	if !ok || got != "topLevel" {
		t.Fatalf("Enclosing = %q, %v; want \"topLevel\", true", got, ok)
	}
}

func TestEnclosingOutsideAnyDeclaration(t *testing.T) {
	p := writeSample(t)
	if _, ok := NewGo().Enclosing(p, 3); ok {
		t.Fatal("blank line between declarations should not resolve")
	}
}

func TestEnclosingUnparseableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.go")
	if err := os.WriteFile(p, []byte("package !!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewGo().Enclosing(p, 1); ok {
		t.Fatal("unparseable file must not resolve")
	}
}

func TestResolveWrapperReturnsEmptyString(t *testing.T) {
	p := writeSample(t)
	if got := Resolve(NewGo(), p, 3); got != "" {
		t.Fatalf("Resolve = %q, want empty", got)
	}
}
