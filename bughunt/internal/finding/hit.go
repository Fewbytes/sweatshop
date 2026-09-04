// Package finding defines the normalized output of every detector and the
// identity function that deduplicates it across runs.
package finding

// Severity ranks a hit. Detector-native severities are mapped onto these three.
type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

var severityRank = map[Severity]int{SeverityLow: 0, SeverityMedium: 1, SeverityHigh: 2}

// AtLeast reports whether s ranks at or above floor. An unknown severity is
// treated as low, so a detector emitting something unexpected is filtered
// rather than promoted.
func (s Severity) AtLeast(floor Severity) bool {
	return severityRank[s] >= severityRank[floor]
}

// Hit is one detector result, normalized across tools.
type Hit struct {
	// RuleID is namespaced by detector, e.g. "govet/printf" or "gosec/G404".
	RuleID string
	File   string
	// Line is display data only and never enters the fingerprint.
	Line     int
	Symbol   string
	Message  string
	Severity Severity
	// MatchText is the source text the detector flagged. It participates in
	// identity, so it must come from the source, not from the tool's prose.
	MatchText string
	// RawRef points at retained raw detector output (an agentsh invocation id),
	// empty when nothing was retained.
	RawRef string
}
