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
	"slices"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/constitution"
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

// Witness binds this daemon to one agent and role for its lifetime and gives
// it a signing key. Everything here must be out of the agent's reach: a config
// the agent could edit would let it pick its own role and key.
type Witness struct {
	KeyFile      string `json:"key_file"`
	Role         string `json:"role"`
	AgentID      string `json:"agent_id"`
	Constitution string `json:"constitution"`
	StateDir     string `json:"state_dir"`
	Remote       string `json:"remote"`
	Ref          string `json:"ref,omitempty"`
}

type Config struct {
	Turso Turso `json:"turso"`

	Witness *Witness `json:"witness,omitempty"`

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
	if err := config.validateWitness(workspaceRoot); err != nil {
		return config, err
	}
	return config, nil
}

func (c *Config) validateWitness(workspaceRoot string) error {
	w := c.Witness
	if w == nil {
		return nil
	}
	fields := []struct {
		name  string
		value *string
		path  bool
	}{
		{"key_file", &w.KeyFile, true}, {"role", &w.Role, false}, {"agent_id", &w.AgentID, false},
		{"constitution", &w.Constitution, true}, {"state_dir", &w.StateDir, true}, {"remote", &w.Remote, true},
	}
	for _, f := range fields {
		if *f.value == "" {
			return fmt.Errorf("config %s: witness.%s is required", c.Path, f.name)
		}
		if !f.path {
			continue
		}
		expanded, err := expandHome(*f.value)
		if err != nil {
			return err
		}
		if *f.value, err = filepath.Abs(expanded); err != nil {
			return err
		}
	}
	if !slices.Contains(constitution.Roles, w.Role) {
		return fmt.Errorf("config %s: witness.role %q is not one of %v", c.Path, w.Role, constitution.Roles)
	}
	if w.Ref == "" {
		w.Ref = "refs/heads/master"
	}
	if workspaceRoot == "" {
		return nil
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return err
	}
	configPath, err := filepath.Abs(c.Path)
	if err != nil {
		return err
	}
	for name, p := range map[string]string{
		"the config file": configPath, "witness.key_file": w.KeyFile, "witness.constitution": w.Constitution,
		"witness.state_dir": w.StateDir, "witness.remote": w.Remote,
	} {
		if within(p, root) {
			return fmt.Errorf("%s (%s) is inside the workspace %s, where the agent can change it; keep witness settings outside", name, p, root)
		}
	}
	return nil
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
