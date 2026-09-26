// Package config loads .depmaint/config.yaml — committed team posture
// (severity floors, ignored packages/ecosystems). An absent file is valid
// and means: no ignores, default severity floor.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config mirrors .depmaint/config.yaml.
type Config struct {
	// SeverityFloor drops advisories below this severity in `audit`: low,
	// medium, or high.
	SeverityFloor string `yaml:"severity_floor"`
	// Ignore holds package names or ecosystem prefixes excluded from audit
	// and hygiene worklists.
	Ignore []string `yaml:"ignore"`
}

// Default returns the configuration used when no file is present.
func Default() Config {
	return Config{SeverityFloor: "low"}
}

// Load reads <dir>/config.yaml. A missing file yields Default().
func Load(dir string) (Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	c := Default()
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.SeverityFloor == "" {
		c.SeverityFloor = "low"
	}
	return c, nil
}
