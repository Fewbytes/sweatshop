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
	if len(c.Ignore) != 0 {
		t.Errorf("Ignore = %v, want empty", c.Ignore)
	}
	if c.SeverityFloor != "low" {
		t.Errorf("SeverityFloor = %q, want low", c.SeverityFloor)
	}
}

func TestLoadReadsFile(t *testing.T) {
	dir := t.TempDir()
	body := "severity_floor = \"high\"\nignore = [\"left-pad\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SeverityFloor != "high" {
		t.Errorf("SeverityFloor = %q, want high", c.SeverityFloor)
	}
	if len(c.Ignore) != 1 || c.Ignore[0] != "left-pad" {
		t.Errorf("Ignore = %v", c.Ignore)
	}
}
