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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Fewbytes/sweatshop/line/attest"
	"github.com/pelletier/go-toml/v2"
)

var Roles = []string{"maker", "andon", "kaizen", "gemba", "viceroy"}

type Constitution struct {
	Trust  Trust  `toml:"trust"`
	Checks Checks `toml:"checks"`
	Runner Runner `toml:"runner"`
	Guards Guards `toml:"guards"`
	// Optional: absent means no king channel, no delegations, no succession.
	Sovereign Sovereign `toml:"sovereign"`
	Channel   Channel   `toml:"channel"`
	Viceroy   Viceroy   `toml:"viceroy"`
	Cards     Cards     `toml:"cards"`
}

// DefaultCommentCap is the comments an agent may leave on its own card per run.
const DefaultCommentCap = 5

// Cards sets what agents may do to their own cards through the station API.
type Cards struct {
	// CommentCap bounds comments per card run; 0 means DefaultCommentCap.
	CommentCap int `toml:"comment_cap,omitempty"`
	// NeedsTimeout (Go duration) is how long a needs:<role> card may sit
	// untouched before heijunka triages it; empty means DefaultNeedsTimeout.
	NeedsTimeout string `toml:"needs_timeout,omitempty"`
}

// DefaultNeedsTimeout is how long a needs:<role> card may wait unanswered.
const DefaultNeedsTimeout = 24 * time.Hour

// NeedsTimeoutOrDefault is the stale-needs timeout with the default applied.
// Parse rejects malformed values, so a bad one here can only come from code.
func (c Cards) NeedsTimeoutOrDefault() time.Duration {
	if d, err := time.ParseDuration(c.NeedsTimeout); err == nil && d > 0 {
		return d
	}
	return DefaultNeedsTimeout
}

// CommentCapOrDefault is the per-run comment cap with the default applied.
func (c Cards) CommentCapOrDefault() int {
	if c.CommentCap == 0 {
		return DefaultCommentCap
	}
	return c.CommentCap
}

// Sovereign is the king: the only identity whose channel messages carry
// authority. Discord is a Discord user ID.
type Sovereign struct {
	Name    string `toml:"name"`
	Discord string `toml:"discord"`
}

// Channel is where the king and the viceroy talk.
type Channel struct {
	Kind    string `toml:"kind"`
	Channel string `toml:"channel"`
	// Transport is how serve receives channel events: TransportPoll (the
	// default when empty) or TransportGateway.
	Transport string `toml:"transport"`
}

const (
	TransportPoll    = "poll"
	TransportGateway = "gateway"
)

type Viceroy struct {
	Succession  bool         `toml:"succession"`
	Delegations []Delegation `toml:"delegations"`
}

// Delegation grants the viceroy one bounded power without the king: either
// offering matching cards (Offer = "line") or closing them (Close). An offer
// matches a card by issue type or by having Parent in its ancestor chain. A
// close matches the listed IDs, descendants of Parent, cards of a listed issue
// type, or cards carrying a listed label. Types are the safer category: any
// agent may set a label, while a card's issue type is harder to game.
type Delegation struct {
	Offer  string   `toml:"offer"`
	Close  bool     `toml:"close"`
	Types  []string `toml:"types"`
	Parent string   `toml:"parent"`
	IDs    []string `toml:"ids"`
	Labels []string `toml:"labels"`
}

// Delegates reports whether some offer delegation covers a card of cardType
// whose ancestor chain is given.
func (c Constitution) Delegates(cardType string, ancestors []string) bool {
	for _, d := range c.Viceroy.Delegations {
		if d.Offer != "line" {
			continue
		}
		if cardType != "" && slices.Contains(d.Types, cardType) {
			return true
		}
		if d.Parent != "" && slices.Contains(ancestors, d.Parent) {
			return true
		}
	}
	return false
}

// DelegatesClose reports whether some close delegation covers card id, given
// its issue type, labels and ancestor chain.
func (c Constitution) DelegatesClose(id, cardType string, labels, ancestors []string) bool {
	for _, d := range c.Viceroy.Delegations {
		if !d.Close {
			continue
		}
		if slices.Contains(d.IDs, id) || (d.Parent != "" && slices.Contains(ancestors, d.Parent)) {
			return true
		}
		if cardType != "" && slices.Contains(d.Types, cardType) {
			return true
		}
		if slices.ContainsFunc(labels, func(l string) bool { return slices.Contains(d.Labels, l) }) {
			return true
		}
	}
	return false
}

var (
	digits = regexp.MustCompile(`^[0-9]+$`)
	cardID = regexp.MustCompile(`^[a-z0-9-]+(\.[0-9]+)*$`)
)

func (c Constitution) validateGovernance() error {
	if c.Cards.CommentCap < 0 {
		return fmt.Errorf("cards.comment_cap %d: must be positive", c.Cards.CommentCap)
	}
	if c.Cards.NeedsTimeout != "" {
		if d, err := time.ParseDuration(c.Cards.NeedsTimeout); err != nil || d <= 0 {
			return fmt.Errorf("cards.needs_timeout %q: want a positive duration like 24h", c.Cards.NeedsTimeout)
		}
	}
	s, ch := c.Sovereign, c.Channel
	if s != (Sovereign{}) && !digits.MatchString(s.Discord) {
		return fmt.Errorf("sovereign.discord %q: want a Discord user ID (digits only)", s.Discord)
	}
	if ch != (Channel{}) {
		if ch.Kind != "discord" {
			return fmt.Errorf("channel.kind %q: want discord", ch.Kind)
		}
		if !digits.MatchString(ch.Channel) {
			return fmt.Errorf("channel.channel %q: want a Discord channel ID (digits only)", ch.Channel)
		}
		if !slices.Contains([]string{"", TransportPoll, TransportGateway}, ch.Transport) {
			return fmt.Errorf("channel.transport %q: want %q or %q", ch.Transport, TransportPoll, TransportGateway)
		}
	}
	for i, d := range c.Viceroy.Delegations {
		if d.Close {
			if d.Offer != "" {
				return fmt.Errorf("viceroy.delegations[%d]: close takes only ids, parent, types or labels", i)
			}
			if len(d.IDs) == 0 && d.Parent == "" && len(d.Types) == 0 && len(d.Labels) == 0 {
				return fmt.Errorf("viceroy.delegations[%d]: close needs ids, parent, types or labels", i)
			}
			for _, id := range d.IDs {
				if !cardID.MatchString(id) {
					return fmt.Errorf("viceroy.delegations[%d].ids %q: not a card id", i, id)
				}
			}
		} else {
			if d.Offer != "line" {
				return fmt.Errorf("viceroy.delegations[%d].offer %q: want line (or close = true)", i, d.Offer)
			}
			if len(d.IDs) > 0 {
				return fmt.Errorf("viceroy.delegations[%d].ids: only a close delegation takes ids", i)
			}
			if len(d.Labels) > 0 {
				return fmt.Errorf("viceroy.delegations[%d].labels: only a close delegation takes labels", i)
			}
			if len(d.Types) == 0 && d.Parent == "" {
				return fmt.Errorf("viceroy.delegations[%d]: needs types or parent", i)
			}
		}
		if slices.Contains(d.Labels, "") {
			return fmt.Errorf("viceroy.delegations[%d].labels: empty label", i)
		}
		if slices.Contains(d.Types, "") {
			return fmt.Errorf("viceroy.delegations[%d].types: empty issue type", i)
		}
		if d.Parent != "" && !cardID.MatchString(d.Parent) {
			return fmt.Errorf("viceroy.delegations[%d].parent %q: not a card id", i, d.Parent)
		}
	}
	return nil
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
	// Timeout bounds each check run (Go duration, default DefaultCheckTimeout).
	// A run that hits it is a runner error: the witness signs nothing, so a
	// hung or starved check can never become signed evidence of failure.
	Timeout string `toml:"timeout,omitempty"`
}

const DefaultCheckTimeout = 30 * time.Minute

// TimeoutDuration is the per-check timeout; empty means the default. Parse
// rejects malformed values, so a bad one here can only come from code.
func (c Checks) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(c.Timeout); err == nil && d > 0 {
		return d
	}
	return DefaultCheckTimeout
}

type Check struct {
	Name    string `toml:"name"`
	Command string `toml:"command"`
}

type Runner struct {
	Kind   string `toml:"kind"`
	Engine string `toml:"engine"`
	Image  string `toml:"image"`
	// Resource ceilings for container check runs (engine syntax: --memory,
	// --pids-limit, --cpus). Empty or zero means the default in Limits.
	Memory string `toml:"memory,omitempty"`
	Pids   int    `toml:"pids,omitempty"`
	CPUs   string `toml:"cpus,omitempty"`
}

// Limits are the resource ceilings a container check run gets.
type Limits struct {
	Memory string
	Pids   int
	CPUs   string
}

const (
	DefaultMemory = "4g"
	DefaultPids   = 1024
	DefaultCPUs   = "2"
)

// Limits returns the runner's ceilings with defaults filled in.
func (r Runner) Limits() Limits {
	l := Limits{Memory: r.Memory, Pids: r.Pids, CPUs: r.CPUs}
	if l.Memory == "" {
		l.Memory = DefaultMemory
	}
	if l.Pids <= 0 {
		l.Pids = DefaultPids
	}
	if l.CPUs == "" {
		l.CPUs = DefaultCPUs
	}
	return l
}

var (
	memorySize = regexp.MustCompile(`^[1-9][0-9]*[bkmgBKMG]?$`) // 0 would mean unlimited
	cpuCount   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// ID is recorded in attestations so the guard can reject checks that ran in
// a different environment than the constitution demands. Resource limits are
// deliberately not part of it: tuning them must not invalidate attestations
// already made under the same image and engine.
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
	if c.Runner.Kind == "container" {
		l := c.Runner.Limits()
		c.Runner.Memory, c.Runner.Pids, c.Runner.CPUs = l.Memory, l.Pids, l.CPUs
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
	if c.Runner.Memory != "" && !memorySize.MatchString(c.Runner.Memory) {
		return fmt.Errorf("runner.memory %q: want a size like 4g or 512m", c.Runner.Memory)
	}
	if c.Runner.Pids < 0 {
		return fmt.Errorf("runner.pids %d: must be positive", c.Runner.Pids)
	}
	if c.Runner.CPUs != "" {
		if n, err := strconv.ParseFloat(c.Runner.CPUs, 64); err != nil || n <= 0 || !cpuCount.MatchString(c.Runner.CPUs) {
			return fmt.Errorf("runner.cpus %q: want a positive number like 2 or 0.5", c.Runner.CPUs)
		}
	}
	if c.Checks.Timeout != "" {
		if d, err := time.ParseDuration(c.Checks.Timeout); err != nil || d <= 0 {
			return fmt.Errorf("checks.timeout %q: want a positive duration like 30m", c.Checks.Timeout)
		}
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
	return c.validateGovernance()
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
