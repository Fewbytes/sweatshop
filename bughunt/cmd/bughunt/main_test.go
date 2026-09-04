package main

import (
	"bytes"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out.Len() == 0 {
		t.Fatal("version printed nothing")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"nope"}, &out, &out)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
