package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesLayout(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatalf("Init: %v", err)
	}
	l := Paths(root)
	for _, dir := range []string{l.Dir, l.Rules, l.Lessons} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("%s is not a directory: %v", dir, err)
		}
	}
	if _, err := os.Stat(l.Config); err != nil {
		t.Errorf("config.yaml not written: %v", err)
	}
	if _, err := os.Stat(l.DB); err != nil {
		t.Errorf("database not created: %v", err)
	}
}

func TestInitWritesSgConfig(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatalf("Init: %v", err)
	}
	l := Paths(root)
	got, err := os.ReadFile(l.SgConfig)
	if err != nil {
		t.Fatalf("sgconfig.yml not written: %v", err)
	}
	want := "ruleDirs:\n  - rules\n"
	if string(got) != want {
		t.Errorf("sgconfig.yml = %q, want %q", got, want)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := Init(root, &out); err != nil {
		t.Fatal(err)
	}
	custom := []byte("severity_floor: high\n")
	if err := os.WriteFile(Paths(root).Config, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	customSg := []byte("ruleDirs:\n  - rules\n  - custom\n")
	if err := os.WriteFile(Paths(root).SgConfig, customSg, 0o644); err != nil {
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
	gotSg, err := os.ReadFile(Paths(root).SgConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSg) != string(customSg) {
		t.Fatal("re-running init must not overwrite an existing sgconfig")
	}
}

func TestInitReportsDetectorAvailability(t *testing.T) {
	var out bytes.Buffer
	if err := Init(t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("govet")) {
		t.Fatalf("init should report on each detector, got:\n%s", out.String())
	}
}

func TestPathsLayout(t *testing.T) {
	l := Paths("/repo")
	if l.Dir != filepath.Join("/repo", ".bughunt") {
		t.Errorf("Dir = %q", l.Dir)
	}
	if l.Rules != filepath.Join("/repo", ".bughunt", "rules") {
		t.Errorf("Rules = %q", l.Rules)
	}
	if l.Lessons != filepath.Join("/repo", ".bughunt", "lessons") {
		t.Errorf("Lessons = %q", l.Lessons)
	}
	if l.SgConfig != filepath.Join("/repo", ".bughunt", "sgconfig.yml") {
		t.Errorf("SgConfig = %q", l.SgConfig)
	}
}
