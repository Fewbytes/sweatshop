package cli

import (
	"bytes"
	"os"
	"testing"
)

func TestInitCreatesLayout(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatalf("Init: %v", err)
	}
	l := Paths(root)
	if info, err := os.Stat(l.Dir); err != nil || !info.IsDir() {
		t.Errorf("%s is not a directory: %v", l.Dir, err)
	}
	if _, err := os.Stat(l.Config); err != nil {
		t.Errorf("config.toml not written: %v", err)
	}
	if _, err := os.Stat(l.DB); err != nil {
		t.Errorf("cache.db not created: %v", err)
	}
}

func TestInitDoesNotOverwriteExistingConfig(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	custom := []byte("severity_floor: high\n")
	if err := os.WriteFile(Paths(root).Config, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(root, &out); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	got, err := os.ReadFile(Paths(root).Config)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatal("re-running init must not overwrite an existing config")
	}
}

func TestInitIsIdempotent(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := Init(root, &out); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}
