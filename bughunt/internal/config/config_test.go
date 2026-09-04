package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SeverityFloor != "low" {
		t.Fatalf("SeverityFloor = %q, want %q", c.SeverityFloor, "low")
	}
	if !c.DetectorEnabled("govet") {
		t.Fatal("govet should be enabled by default")
	}
}

func TestLoadDisablesDetector(t *testing.T) {
	dir := t.TempDir()
	body := "detectors:\n  gosec: false\nseverity_floor: medium\nexclude:\n  - vendor/**\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DetectorEnabled("gosec") {
		t.Fatal("gosec should be disabled")
	}
	if !c.DetectorEnabled("govet") {
		t.Fatal("govet unmentioned, should stay enabled")
	}
	if c.SeverityFloor != "medium" {
		t.Fatalf("SeverityFloor = %q, want medium", c.SeverityFloor)
	}
	if !c.Excluded("vendor/github.com/x/y.go") {
		t.Fatal("vendor path should be excluded")
	}
	if c.Excluded("internal/scan/scan.go") {
		t.Fatal("internal path should not be excluded")
	}
}

func TestLoadEmptyFileIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.DetectorEnabled("staticcheck") {
		t.Fatal("empty config means auto-detect everything")
	}
}
