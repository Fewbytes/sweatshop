// Package constitution loads the line's rules. v0 holds only what the witness
// and merge guard need: trusted witness keys, required checks, the check
// runner, and role-gated paths.
package constitution

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/pelletier/go-toml/v2"
)

var Roles = []string{"maker", "andon", "kaizen", "gemba"}

type Constitution struct {
	Trust  Trust  `toml:"trust"`
	Checks Checks `toml:"checks"`
	Runner Runner `toml:"runner"`
	Guards Guards `toml:"guards"`
}

// Trust.Level labels how far check environments can be trusted. Signatures
// are verified at every level; "none" only permits host-local check runs.
type Trust struct {
	Level string       `toml:"level"`
	Keys  []TrustedKey `toml:"keys"`
}

type TrustedKey struct {
	Name      string `toml:"name"`
	PublicKey string `toml:"public_key"`
	// Roles limits which roles this key may attest as. Empty means any role.
	Roles []string `toml:"roles"`
}

type Checks struct {
	Required []Check `toml:"required"`
}

type Check struct {
	Name    string `toml:"name"`
	Command string `toml:"command"`
}

type Runner struct {
	Kind   string `toml:"kind"`
	Engine string `toml:"engine"`
	Image  string `toml:"image"`
}

// ID is recorded in attestations so the guard can reject checks that ran in
// a different environment than the constitution demands.
func (r Runner) ID() string {
	if r.Kind == "container" {
		return "container:" + r.Engine + ":" + r.Image
	}
	return "local"
}

type Guards struct {
	Paths []PathGuard `toml:"paths"`
}

type PathGuard struct {
	Pattern string   `toml:"pattern"`
	Roles   []string `toml:"roles"`
}

func Load(file string) (Constitution, string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Constitution{}, "", err
	}
	c, err := Parse(data)
	if err != nil {
		return Constitution{}, "", fmt.Errorf("%s: %w", file, err)
	}
	sum := sha256.Sum256(data)
	return c, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func Parse(data []byte) (Constitution, error) {
	var c Constitution
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Constitution{}, fmt.Errorf("parse constitution: %w", err)
	}
	if c.Runner.Kind == "container" && c.Runner.Engine == "" {
		c.Runner.Engine = "docker"
	}
	if err := c.validate(); err != nil {
		return Constitution{}, err
	}
	return c, nil
}

func (c Constitution) validate() error {
	switch c.Runner.Kind {
	case "local":
	case "container":
		if c.Runner.Image == "" {
			return errors.New("runner kind container requires runner.image")
		}
	default:
		return fmt.Errorf("runner.kind %q: want local or container", c.Runner.Kind)
	}
	switch c.Trust.Level {
	case "none":
	case "attest":
		if c.Runner.Kind != "container" {
			return errors.New("trust level attest requires runner kind container")
		}
	default:
		return fmt.Errorf("trust.level %q: want none or attest", c.Trust.Level)
	}
	if len(c.Checks.Required) == 0 {
		return errors.New("checks.required is empty; the merge guard would accept anything")
	}
	seen := map[string]bool{}
	for _, check := range c.Checks.Required {
		if check.Name == "" || check.Command == "" {
			return errors.New("every required check needs a name and a command")
		}
		if seen[check.Name] {
			return fmt.Errorf("duplicate required check %q", check.Name)
		}
		seen[check.Name] = true
	}
	if _, err := c.TrustedKeys(); err != nil {
		return err
	}
	for _, k := range c.Trust.Keys {
		for _, role := range k.Roles {
			if !slices.Contains(Roles, role) {
				return fmt.Errorf("trust key %q: unknown role %q", k.Name, role)
			}
		}
	}
	for _, g := range c.Guards.Paths {
		if g.Pattern == "" || len(g.Roles) == 0 {
			return errors.New("every path guard needs a pattern and at least one role")
		}
		// Validate pattern syntax for non-/** patterns
		if !strings.HasSuffix(g.Pattern, "/**") {
			_, err := path.Match(g.Pattern, "")
			if err != nil {
				return fmt.Errorf("path guard pattern %q: %w", g.Pattern, err)
			}
		}
		for _, role := range g.Roles {
			if !slices.Contains(Roles, role) {
				return fmt.Errorf("path guard %q: unknown role %q", g.Pattern, role)
			}
		}
	}
	return nil
}

func (c Constitution) TrustedKeys() (map[string]ed25519.PublicKey, error) {
	keys := make(map[string]ed25519.PublicKey, len(c.Trust.Keys))
	for _, k := range c.Trust.Keys {
		pub, err := attest.ParsePublicKey(k.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("trust key %q: %w", k.Name, err)
		}
		keys[attest.KeyID(pub)] = pub
	}
	return keys, nil
}

// KeyRoles maps key ID to the roles that key may attest as. A key with an
// empty list may attest as any role.
func (c Constitution) KeyRoles() (map[string][]string, error) {
	roles := make(map[string][]string, len(c.Trust.Keys))
	for _, k := range c.Trust.Keys {
		pub, err := attest.ParsePublicKey(k.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("trust key %q: %w", k.Name, err)
		}
		roles[attest.KeyID(pub)] = k.Roles
	}
	return roles, nil
}

// AllowedRoles returns the roles permitted to change p, and whether any path
// guard covers it. The first matching guard wins.
func (c Constitution) AllowedRoles(p string) ([]string, bool) {
	for _, g := range c.Guards.Paths {
		if MatchPath(g.Pattern, p) {
			return g.Roles, true
		}
	}
	return nil, false
}

// MatchPath matches slash-separated repository paths with path.Match; a
// trailing "/**" matches a directory and everything beneath it.
func MatchPath(pattern, p string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	ok, err := path.Match(pattern, p)
	return err == nil && ok
}
