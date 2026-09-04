package finding

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Fingerprint is the stable identity of a hit: the rule that fired, the
// declaration that contains it, and the code that matched. Line numbers and
// file paths are deliberately excluded so a finding survives edits above it and
// travels with a function moved to another file. A renamed enclosing symbol
// yields a new fingerprint, which is intended — renamed code deserves a fresh
// look.
//
// When no enclosing symbol could be resolved, the file path anchors the
// fingerprint instead. That is weaker, but it is the only stable handle left.
func Fingerprint(h Hit) string {
	anchor := h.Symbol
	if anchor == "" {
		anchor = h.File
	}
	sum := sha256.Sum256([]byte(h.RuleID + "\x00" + anchor + "\x00" + Normalize(h.MatchText)))
	return hex.EncodeToString(sum[:])[:16]
}

// Normalize collapses whitespace runs to single spaces and trims the result, so
// reformatting does not change identity.
func Normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
