package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestScanOnCleanTreeExitsZero(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()

	// No Go files and no rules: every detector either finds nothing or is
	// unavailable, so the gate must stay open.
	code, err := Scan(context.Background(), root, nil, &out, &out)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, out.String())
	}
}

func TestScanWithoutInitIsAUsageError(t *testing.T) {
	var out bytes.Buffer
	code, err := Scan(context.Background(), t.TempDir(), nil, &out, &out)
	if err == nil {
		t.Fatal("scanning an uninitialized repo must report an error")
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestScanRejectsUnknownFlag(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatal(err)
	}
	code, _ := Scan(context.Background(), root, []string{"--nope"}, &out, &out)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
