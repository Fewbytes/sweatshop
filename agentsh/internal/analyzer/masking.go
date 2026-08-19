package analyzer

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// This file masks recognizable dynamic values (timestamps, IDs, numbers...)
// out of a log line and clusters lines by the resulting masked string plus a
// hash. It is deliberately not the Drain algorithm: there's no fixed-depth
// parse tree, token-position similarity threshold, or cluster merging, so a
// variable this regex set doesn't recognize produces a distinct template
// rather than collapsing into an existing one. Naming reflects that; see
// sweatshop-nz4.
var (
	urlRegex       = regexp.MustCompile(`https?://\S+`)
	isoTimeRegex   = regexp.MustCompile(`\b\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`)
	shortTimeRegex = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	uuidRegex      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	ipRegex        = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d+)?\b`)
	hexRegex       = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`)
	pathRegex      = regexp.MustCompile(`(?:/[a-zA-Z0-9_.-]+){2,}`)
	// The bare \b\d+\b alternative used to be listed as a second branch
	// after \b\d+(?:unit)?\b, but that branch already matches bare digits
	// on its own (the unit suffix is optional) — the second branch was dead.
	numRegex = regexp.MustCompile(`\b\d+(?:ms|s|us|µs|ns|m|h|B|KB|MB|GB|TB|b|kb|mb|gb|tb)?\b`)

	// maskOrder lists the patterns in priority order (matched into a single
	// combined alternation below) alongside their placeholder. Order matters:
	// it's the same precedence the old sequential passes applied, preserved
	// so e.g. a timestamp inside a URL is masked as part of the URL, not
	// separately, exactly as when each pass ran on the previous pass's
	// already-masked output.
	maskOrder = []struct {
		re          *regexp.Regexp
		placeholder string
	}{
		{urlRegex, "<URL>"},
		{isoTimeRegex, "<TIME>"},
		{shortTimeRegex, "<TIME>"},
		{uuidRegex, "<UUID>"},
		{ipRegex, "<IP>"},
		{hexRegex, "<HEX>"},
		{pathRegex, "<PATH>"},
		{numRegex, "<NUM>"},
	}

	// maskRegex is every pattern above joined into one alternation, so a
	// line is scanned once instead of once per category. Go's regexp
	// alternation is leftmost-first: at a given starting position it tries
	// branches in the order written, so this preserves maskOrder's
	// precedence without needing multiple passes.
	maskRegex = regexp.MustCompile(joinPatterns())
)

func joinPatterns() string {
	parts := make([]string, len(maskOrder))
	for i, m := range maskOrder {
		parts[i] = m.re.String()
	}
	return strings.Join(parts, "|")
}

// MaskVariables replaces dynamic variables in a log line with generic placeholders.
func MaskVariables(line string) string {
	return maskRegex.ReplaceAllStringFunc(line, maskToken)
}

// maskToken picks the placeholder for one already-matched token by re-testing
// it against each category in priority order. Each check runs against the
// short matched token, not the full line, so this stays cheap even though
// it's a second pass of matching.
func maskToken(token string) string {
	for _, m := range maskOrder {
		if m.re.MatchString(token) {
			return m.placeholder
		}
	}
	return token // unreachable: token was produced by maskRegex itself
}

// GenerateTemplateID creates a stable 16-character hex identifier for a template string.
func GenerateTemplateID(template string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(template)))
	return hex.EncodeToString(sum[:8])
}

// TemplateCluster clusters log lines into templates using variable masking.
type TemplateCluster struct {
	ID             string
	Template       string
	Count          int
	FirstLine      int
	LastLine       int
	ExemplarOffset int64
	Exemplar       string
	Level          string
}

// ClusterLines groups a slice of log lines into template clusters. Lines
// absorbed into a stack trace block are filtered out by the caller before
// this runs — every cluster here is by construction not a stack trace, which
// is why TemplateCluster carries no IsStackTrace field (LogTemplate's is set
// explicitly by analyzer.go for both paths instead).
func ClusterLines(lines []string, offsets []int64) []TemplateCluster {
	clusters := make(map[string]*TemplateCluster)
	var order []string

	for idx, line := range lines {
		lineNum := idx + 1
		var offset int64
		if idx < len(offsets) {
			offset = offsets[idx]
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		masked := MaskVariables(trimmed)
		id := GenerateTemplateID(masked)

		if existing, found := clusters[id]; found {
			existing.Count++
			existing.LastLine = lineNum
		} else {
			// Extract log level if recognizable in the line.
			level := DetectLogLevel(trimmed)
			c := &TemplateCluster{
				ID:             id,
				Template:       masked,
				Count:          1,
				FirstLine:      lineNum,
				LastLine:       lineNum,
				ExemplarOffset: offset,
				Exemplar:       trimmed,
				Level:          level,
			}
			clusters[id] = c
			order = append(order, id)
		}
	}

	result := make([]TemplateCluster, 0, len(order))
	for _, id := range order {
		result = append(result, *clusters[id])
	}
	return result
}
