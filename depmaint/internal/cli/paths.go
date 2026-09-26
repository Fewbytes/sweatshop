package cli

import "path/filepath"

// Layout is the set of paths depmaint reads and writes under a repo root.
type Layout struct {
	Dir    string // .depmaint
	Config string // .depmaint/config.yaml
	DB     string // .depmaint/cache.db
}

// Paths resolves depmaint's layout relative to a repo root.
func Paths(root string) Layout {
	dir := filepath.Join(root, ".depmaint")
	return Layout{
		Dir:    dir,
		Config: filepath.Join(dir, "config.yaml"),
		DB:     filepath.Join(dir, "cache.db"),
	}
}
