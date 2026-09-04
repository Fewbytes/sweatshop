// Package config loads .bughunt/config.yaml. An absent or empty file is valid
// and means: enable every detector that is installed, exclude nothing.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config mirrors .bughunt/config.yaml.
type Config struct {
	// Detectors maps a detector name to whether it is enabled. A detector
	// absent from the map is enabled.
	Detectors map[string]bool `yaml:"detectors"`
	// SeverityFloor drops hits below this severity: low, medium, or high.
	SeverityFloor string `yaml:"severity_floor"`
	// Exclude holds glob patterns matched against slash-separated repo paths.
	// A trailing /** matches the directory and everything under it.
	Exclude []string `yaml:"exclude"`
}

// Default returns the configuration used when no file is present.
func Default() Config {
	return Config{Detectors: map[string]bool{}, SeverityFloor: "low"}
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
	if c.Detectors == nil {
		c.Detectors = map[string]bool{}
	}
	if c.SeverityFloor == "" {
		c.SeverityFloor = "low"
	}
	return c, nil
}

// DetectorEnabled reports whether the named detector should run.
func (c Config) DetectorEnabled(name string) bool {
	enabled, ok := c.Detectors[name]
	return !ok || enabled
}

// Excluded reports whether a repo-relative path matches an exclude pattern.
func (c Config) Excluded(p string) bool {
	p = filepath.ToSlash(p)
	for _, pattern := range c.Exclude {
		if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
			if p == dir || strings.HasPrefix(p, dir+"/") {
				return true
			}
			continue
		}
		if ok, _ := path.Match(pattern, p); ok {
			return true
		}
	}
	return false
}
