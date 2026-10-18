package planner

import (
	"fmt"
	"strconv"
	"strings"
)

// Tier orders upgrade sizes after Renovate's grouping semantics: patch-only
// sets are tried first, minor-coordinated sets next, majors last.
type Tier int

const (
	TierPatch Tier = iota
	TierMinor
	TierMajor
)

func (t Tier) String() string {
	switch t {
	case TierPatch:
		return "patch"
	case TierMinor:
		return "minor"
	case TierMajor:
		return "major"
	}
	panic(fmt.Sprintf("planner: unknown tier %d", int(t)))
}

// Version is a numeric major.minor.patch triple; pre-release and build
// suffixes are rejected rather than silently ignored.
type Version struct{ Major, Minor, Patch int }

// ParseVersion accepts "1", "1.2" and "1.2.3", each with an optional "v".
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) > 3 {
		return Version{}, fmt.Errorf("version %q: want at most major.minor.patch", s)
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("version %q: component %q is not a number", s, p)
		}
		nums[i] = n
	}
	return Version{nums[0], nums[1], nums[2]}, nil
}

// Compare returns -1, 0 or 1.
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

// TierOf classifies the move from one version to a newer one by the most
// significant component that changed.
func TierOf(from, to Version) Tier {
	switch {
	case from.Major != to.Major:
		return TierMajor
	case from.Minor != to.Minor:
		return TierMinor
	}
	return TierPatch
}

// SemverAccepts reports whether version satisfies requirement, a list of
// clauses separated by spaces or commas that must all hold. A clause is "*",
// or an optional operator (>=, <=, >, <, =, ^, ~) followed by a version; a
// bare version means equality. A malformed requirement is an error, never a
// silent accept.
func SemverAccepts(requirement, version string) (bool, error) {
	v, err := ParseVersion(version)
	if err != nil {
		return false, err
	}
	clauses := strings.FieldsFunc(requirement, func(r rune) bool { return r == ' ' || r == ',' })
	if len(clauses) == 0 {
		return false, fmt.Errorf("empty requirement")
	}
	for _, c := range clauses {
		ok, err := clauseAccepts(c, v)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func clauseAccepts(clause string, v Version) (bool, error) {
	if clause == "*" {
		return true, nil
	}
	op := ""
	for _, candidate := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(clause, candidate) {
			op = candidate
			break
		}
	}
	bound, err := ParseVersion(strings.TrimPrefix(clause, op))
	if err != nil {
		return false, fmt.Errorf("requirement clause %q: %w", clause, err)
	}
	cmp := v.Compare(bound)
	switch op {
	case ">=":
		return cmp >= 0, nil
	case "<=":
		return cmp <= 0, nil
	case ">":
		return cmp > 0, nil
	case "<":
		return cmp < 0, nil
	case "", "=":
		return cmp == 0, nil
	case "~":
		return cmp >= 0 && v.Major == bound.Major && v.Minor == bound.Minor, nil
	}
	// "^": may not change the left-most non-zero component.
	switch {
	case bound.Major > 0:
		return cmp >= 0 && v.Major == bound.Major, nil
	case bound.Minor > 0:
		return cmp >= 0 && v.Major == 0 && v.Minor == bound.Minor, nil
	}
	return cmp == 0, nil
}
