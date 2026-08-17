package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string, mode os.FileMode) string {
	t.Helper()
	stateDir := filepath.Join(dir, ".agentsh")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "config.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// Running with no config at all is the normal local case.
func TestMissingConfigIsNotAnError(t *testing.T) {
	settings, err := Load("", t.TempDir())
	if err != nil {
		t.Fatalf("missing config should be fine: %v", err)
	}
	if settings.Turso.URL != "" || settings.Path != "" {
		t.Errorf("unexpected settings: %+v", settings)
	}
}

// An explicitly requested file that is absent must fail loudly rather than
// starting with settings the operator did not ask for.
func TestExplicitMissingConfigIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json"), ""); err == nil {
		t.Fatal("expected an error for a missing explicit config path")
	}
}

func TestWorkspaceConfigIsLoaded(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `{"turso":{"url":"libsql://example","auth_token":"secret","sync_interval_seconds":30}}`, 0o600)

	settings, err := Load("", root)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Turso.URL != "libsql://example" || settings.Turso.AuthToken != "secret" {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	if settings.SyncInterval().Seconds() != 30 {
		t.Errorf("sync interval = %v", settings.SyncInterval())
	}
}

// The daemon must not take its settings from the environment: whatever lives
// there is inherited by every supervised command.
func TestEnvironmentIsIgnoredAndReported(t *testing.T) {
	t.Setenv("TURSO_DATABASE_URL", "libsql://from-env")
	t.Setenv("TURSO_AUTH_TOKEN", "token-from-env")

	settings, err := Load("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Turso.URL != "" || settings.Turso.AuthToken != "" {
		t.Fatalf("environment leaked into settings: %+v", settings.Turso)
	}
	if len(settings.Warnings) != 2 {
		t.Errorf("expected both legacy variables reported, got %v", settings.Warnings)
	}
}

func TestAuthTokenFileIsRead(t *testing.T) {
	root := t.TempDir()
	tokenPath := filepath.Join(root, "token")
	if err := os.WriteFile(tokenPath, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, `{"turso":{"url":"libsql://example","auth_token_file":"`+tokenPath+`"}}`, 0o600)

	settings, err := Load("", root)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Turso.AuthToken != "file-token" {
		t.Errorf("auth token = %q", settings.Turso.AuthToken)
	}
}

func TestTokenAndTokenFileTogetherIsAnError(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `{"turso":{"url":"u","auth_token":"a","auth_token_file":"/tmp/x"}}`, 0o600)
	if _, err := Load("", root); err == nil {
		t.Fatal("expected an error when both token forms are set")
	}
}

func TestTokenWithoutURLIsAnError(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `{"turso":{"auth_token":"orphan"}}`, 0o600)
	if _, err := Load("", root); err == nil {
		t.Fatal("expected an error for a token with no url")
	}
}

func TestBrokenConfigReportsItsPath(t *testing.T) {
	root := t.TempDir()
	path := writeConfig(t, root, `{"turso":`, 0o600)
	_, err := Load("", root)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error does not name the file: %v", err)
	}
}

func TestOverlyReadableCredentialFileWarns(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `{"turso":{"url":"libsql://example","auth_token":"secret"}}`, 0o644)

	settings, err := Load("", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Warnings) == 0 {
		t.Error("a world-readable credential file should warn")
	}
}
