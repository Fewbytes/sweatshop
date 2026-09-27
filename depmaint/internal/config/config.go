// Package config loads .depmaint/config.toml — committed team posture
// (severity floors, ignored packages/ecosystems). An absent file is valid
// and means: no ignores, default severity floor.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Config mirrors .depmaint/config.toml.
type Config struct {
	// SeverityFloor drops advisories below this severity in `audit`: low,
	// medium, or high.
	SeverityFloor string `toml:"severity_floor"`
	// Ignore holds package names or ecosystem prefixes excluded from audit
	// and hygiene worklists.
	Ignore []string `toml:"ignore"`
}

// Default returns the configuration used when no file is present.
func Default() Config {
	return Config{SeverityFloor: "low"}
}

// Load reads <dir>/config.toml. A missing file yields Default().
func Load(dir string) (Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	c := Default()
	if err := toml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.SeverityFloor == "" {
		c.SeverityFloor = "low"
	}
	return c, nil
}
