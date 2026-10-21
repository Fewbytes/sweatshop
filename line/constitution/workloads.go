package constitution

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"time"
)

// Workload kinds a rule may cover (design-docs/line-controller.md, Policy).
const (
	WorkloadKindTest    = "test"
	WorkloadKindStaging = "staging"
	WorkloadKindDemo    = "demo"
	WorkloadKindBuild   = "build"
)

// WorkloadKinds lists every kind a rule may name.
var WorkloadKinds = []string{WorkloadKindTest, WorkloadKindStaging, WorkloadKindDemo, WorkloadKindBuild}

// Workload approval modes: auto approves a request inside its rule, king
// posts it to the channel for the king's decision.
const (
	WorkloadApprovalAuto = "auto"
	WorkloadApprovalKing = "king"
)

// Workloads is the policy for workload requests stations make of the line
// controller. It is default deny: a request no rule covers is refused.
type Workloads struct {
	Rules []WorkloadRule `toml:"rules"`
	// Images lists the image references a request may name, as path.Match
	// patterns (such as "localhost/*"). Empty means no image is allowed.
	Images []string `toml:"images"`
}

// WorkloadRule allows the roles to run workloads of the kinds, within the
// limits it sets. Resource fields and TTL are upper bounds; TTL is required
// because every workload expires.
type WorkloadRule struct {
	Roles    []string `toml:"roles"`
	Kinds    []string `toml:"kinds"`
	Approval string   `toml:"approval"`
	// MaxPerStation caps live workloads of one station under this rule;
	// 0 means no cap.
	MaxPerStation int `toml:"max_per_station,omitempty"`
	// Memory (like 4g), CPUs (like 2 or 0.5) and Pids bound a request's
	// resources; empty or 0 means the rule sets no bound.
	Memory string `toml:"memory,omitempty"`
	CPUs   string `toml:"cpus,omitempty"`
	Pids   int    `toml:"pids,omitempty"`
	// TTL (Go duration) is the longest a workload may live.
	TTL string `toml:"ttl"`
	// Expose lets a request publish a port.
	Expose bool `toml:"expose,omitempty"`
}

// TTLDuration is the rule's lifetime bound. Parse rejects malformed values,
// so a bad one here can only come from code.
func (r WorkloadRule) TTLDuration() time.Duration {
	d, err := time.ParseDuration(r.TTL)
	if err != nil {
		panic(fmt.Sprintf("workload rule ttl %q: %v", r.TTL, err))
	}
	return d
}

// RuleFor returns the first rule covering role and kind; false means the
// request is denied.
func (w Workloads) RuleFor(role, kind string) (WorkloadRule, bool) {
	for _, r := range w.Rules {
		if slices.Contains(r.Roles, role) && slices.Contains(r.Kinds, kind) {
			return r, true
		}
	}
	return WorkloadRule{}, false
}

// ImageAllowed reports whether image matches an entry of Images.
func (w Workloads) ImageAllowed(image string) bool {
	return slices.ContainsFunc(w.Images, func(pattern string) bool {
		ok, err := path.Match(pattern, image)
		return err == nil && ok
	})
}

func (c Constitution) validateWorkloads() error {
	for i, r := range c.Workloads.Rules {
		if len(r.Roles) == 0 {
			return fmt.Errorf("workloads.rules[%d].roles: must list at least one role", i)
		}
		for _, role := range r.Roles {
			if !slices.Contains(Roles, role) {
				return fmt.Errorf("workloads.rules[%d].roles %q: not a role", i, role)
			}
		}
		if len(r.Kinds) == 0 {
			return fmt.Errorf("workloads.rules[%d].kinds: must list at least one kind", i)
		}
		for _, kind := range r.Kinds {
			if !slices.Contains(WorkloadKinds, kind) {
				return fmt.Errorf("workloads.rules[%d].kinds %q: want one of %v", i, kind, WorkloadKinds)
			}
		}
		if r.Approval != WorkloadApprovalAuto && r.Approval != WorkloadApprovalKing {
			return fmt.Errorf("workloads.rules[%d].approval %q: want %s or %s", i, r.Approval, WorkloadApprovalAuto, WorkloadApprovalKing)
		}
		if r.MaxPerStation < 0 {
			return fmt.Errorf("workloads.rules[%d].max_per_station %d: must not be negative", i, r.MaxPerStation)
		}
		if r.Memory != "" && !memorySize.MatchString(r.Memory) {
			return fmt.Errorf("workloads.rules[%d].memory %q: want a size like 4g or 512m", i, r.Memory)
		}
		if r.CPUs != "" {
			if n, err := strconv.ParseFloat(r.CPUs, 64); err != nil || n <= 0 || !cpuCount.MatchString(r.CPUs) {
				return fmt.Errorf("workloads.rules[%d].cpus %q: want a positive number like 2 or 0.5", i, r.CPUs)
			}
		}
		if r.Pids < 0 {
			return fmt.Errorf("workloads.rules[%d].pids %d: must not be negative", i, r.Pids)
		}
		if d, err := time.ParseDuration(r.TTL); err != nil || d <= 0 {
			return fmt.Errorf("workloads.rules[%d].ttl %q: want a positive duration like 2h", i, r.TTL)
		}
		for _, other := range c.Workloads.Rules[:i] {
			for _, role := range r.Roles {
				for _, kind := range r.Kinds {
					if slices.Contains(other.Roles, role) && slices.Contains(other.Kinds, kind) {
						return fmt.Errorf("workloads.rules[%d]: role %q kind %q is already covered by an earlier rule", i, role, kind)
					}
				}
			}
		}
	}
	for i, pattern := range c.Workloads.Images {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("workloads.images[%d] %q: %w", i, pattern, err)
		}
	}
	return nil
}
