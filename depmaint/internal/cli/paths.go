package cli

import "path/filepath"

// Layout is the set of paths depmaint reads and writes under a repo root.
type Layout struct {
	Dir    string // .depmaint
	Config string // .depmaint/config.toml
	DB     string // .depmaint/cache.db
	// Campaigns is the campaign database: decisions, not cache.
	Campaigns string // .depmaint/campaign.db
	// Dolt is the synced campaign database working directory.
	Dolt string // .depmaint/dolt
}

// Paths resolves depmaint's layout relative to a repo root.
func Paths(root string) Layout {
	dir := filepath.Join(root, ".depmaint")
	return Layout{
		Dir:       dir,
		Config:    filepath.Join(dir, "config.toml"),
		DB:        filepath.Join(dir, "cache.db"),
		Campaigns: filepath.Join(dir, "campaign.db"),
		Dolt:      filepath.Join(dir, "dolt"),
	}
}
