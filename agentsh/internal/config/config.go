// Package config loads agentsh daemon settings from a file.
//
// The daemon deliberately does not read its own configuration from the
// environment. Its environment is inherited by every command it supervises, so
// anything kept there — a Turso auth token above all — is handed to arbitrary
// agent-run processes and echoed into invocation records.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LegacyEnvVars were read directly by the daemon before settings moved to a
// file. They are reported as ignored rather than honoured silently.
var LegacyEnvVars = []string{"TURSO_DATABASE_URL", "TURSO_AUTH_TOKEN"}

type Turso struct {
	// URL is the remote database. Empty keeps storage purely local.
	URL string `json:"url,omitempty"`

	// AuthToken is the token itself. Prefer AuthTokenFile when the config file
	// is shared or otherwise more readable than the secret should be.
	AuthToken string `json:"auth_token,omitempty"`

	// AuthTokenFile is a path to read the token from. A leading ~ is expanded.
	AuthTokenFile string `json:"auth_token_file,omitempty"`

	// SyncIntervalSeconds controls embedded replica sync. Zero disables it.
	SyncIntervalSeconds int `json:"sync_interval_seconds,omitempty"`
}

type Config struct {
	Turso Turso `json:"turso"`

	// Path is the file this came from, empty when no file was found.
	Path string `json:"-"`

	// Warnings are non-fatal problems worth surfacing to the operator.
	Warnings []string `json:"-"`
}

func (c Config) SyncInterval() time.Duration {
	if c.Turso.SyncIntervalSeconds <= 0 {
		return 0
	}
	return time.Duration(c.Turso.SyncIntervalSeconds) * time.Second
}

// Candidates lists the files consulted, in precedence order.
func Candidates(explicitPath, workspaceRoot string) []string {
	if explicitPath != "" {
		return []string{explicitPath}
	}
	var paths []string
	if workspaceRoot != "" {
		paths = append(paths, filepath.Join(workspaceRoot, ".agentsh", "config.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "agentsh", "config.json"))
	}
	return paths
}

// Load reads the first config file that exists. A missing file is not an error:
// agentsh runs fully local without any configuration.
//
// An explicitly requested path that does not exist IS an error, since silently
// ignoring it would run with settings the operator did not ask for.
func Load(explicitPath, workspaceRoot string) (Config, error) {
	var config Config
	for _, name := range LegacyEnvVars {
		if os.Getenv(name) != "" {
			config.Warnings = append(config.Warnings, fmt.Sprintf(
				"%s is set but ignored; the daemon reads settings from a config file so its environment is not passed to supervised commands", name))
		}
	}

	for index, path := range Candidates(explicitPath, workspaceRoot) {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) && explicitPath == "" {
				continue
			}
			if os.IsNotExist(err) && index == 0 && explicitPath != "" {
				return config, fmt.Errorf("config file %s does not exist", path)
			}
			return config, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return config, fmt.Errorf("parse config %s: %w", path, err)
		}
		config.Path = path
		if warning := permissionWarning(path, config); warning != "" {
			config.Warnings = append(config.Warnings, warning)
		}
		break
	}

	if err := config.resolveToken(); err != nil {
		return config, err
	}
	if config.Turso.AuthToken != "" && config.Turso.URL == "" {
		return config, fmt.Errorf("config %s sets turso.auth_token without turso.url", config.Path)
	}
	return config, nil
}

func (c *Config) resolveToken() error {
	if c.Turso.AuthTokenFile == "" {
		return nil
	}
	if c.Turso.AuthToken != "" {
		return fmt.Errorf("config %s sets both turso.auth_token and turso.auth_token_file", c.Path)
	}
	path, err := expandHome(c.Turso.AuthTokenFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read turso.auth_token_file: %w", err)
	}
	c.Turso.AuthToken = strings.TrimSpace(string(data))
	if c.Turso.AuthToken == "" {
		return fmt.Errorf("turso.auth_token_file %s is empty", path)
	}
	return nil
}

func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

// permissionWarning reports a credential file readable beyond its owner.
func permissionWarning(path string, config Config) string {
	if config.Turso.AuthToken == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Sprintf("%s holds a credential but is mode %#o; chmod 600 it", path, mode)
	}
	return ""
}
