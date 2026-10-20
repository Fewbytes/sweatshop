package secret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCred(t *testing.T, dir, name, value string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

func TestForLinePrefersSystemdThenEnvThenFile(t *testing.T) {
	lineDir, credDir := t.TempDir(), t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	writeCred(t, lineDir, ModelCred, "from-file\n", 0o600)

	get := func() string {
		t.Helper()
		v, err := ForLine(lineDir).Read(ModelCred)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := get(); got != "from-file" {
		t.Fatalf("file only: got %q", got)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", " from-env \n")
	if got := get(); got != "from-env" {
		t.Fatalf("env beats file: got %q", got)
	}
	writeCred(t, credDir, ModelCred, "from-systemd\n", 0o400)
	t.Setenv("CREDENTIALS_DIRECTORY", credDir)
	if got := get(); got != "from-systemd" {
		t.Fatalf("systemd beats env: got %q", got)
	}
}

func TestForLineReportsNotFoundWhenNoSourceHasIt(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("SWEATSHOP_DISCORD_TOKEN", "")
	_, err := ForLine(t.TempDir()).Read(DiscordCred)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v; want ErrNotFound", err)
	}
}

func TestFileRejectsLooseModeAndEmptyWithoutLeakingValue(t *testing.T) {
	dir := t.TempDir()
	writeCred(t, dir, "loose.cred", "s3cret-value", 0o644)
	_, err := File{Dir: dir}.Read("loose.cred")
	if err == nil || !strings.Contains(err.Error(), "want 0600") || strings.Contains(err.Error(), "s3cret-value") {
		t.Fatalf("loose mode: got %v", err)
	}
	writeCred(t, dir, "empty.cred", " \n", 0o600)
	if _, err := (File{Dir: dir}).Read("empty.cred"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("empty file: got %v; want a non-NotFound error", err)
	}
}
