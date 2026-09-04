// Package cli implements the bughunt commands.
package cli

import "path/filepath"

// Dir is the per-repository state directory.
const Dir = ".bughunt"

// Layout names every path bughunt owns inside a repository.
type Layout struct {
	Dir      string
	Config   string
	Rules    string
	Lessons  string
	DB       string
	SgConfig string
}

// Paths derives the layout for a repository root.
func Paths(root string) Layout {
	base := filepath.Join(root, Dir)
	return Layout{
		Dir:      base,
		Config:   filepath.Join(base, "config.yaml"),
		Rules:    filepath.Join(base, "rules"),
		Lessons:  filepath.Join(base, "lessons"),
		DB:       filepath.Join(base, "bughunt.db"),
		SgConfig: filepath.Join(base, "sgconfig.yml"),
	}
}
