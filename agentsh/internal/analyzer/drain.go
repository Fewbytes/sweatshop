package analyzer

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	urlRegex       = regexp.MustCompile(`https?://\S+`)
	isoTimeRegex   = regexp.MustCompile(`\b\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`)
	shortTimeRegex = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	uuidRegex      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	ipRegex        = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d+)?\b`)
	hexRegex       = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`)
	pathRegex      = regexp.MustCompile(`(?:/[a-zA-Z0-9_.-]+){2,}`)
	numRegex       = regexp.MustCompile(`\b\d+(?:ms|s|us|µs|ns|m|h|B|KB|MB|GB|TB|b|kb|mb|gb|tb)?\b|\b\d+\b`)
)

// MaskVariables replaces dynamic variables in a log line with generic placeholders.
func MaskVariables(line string) string {
	s := urlRegex.ReplaceAllString(line, "<URL>")
	s = isoTimeRegex.ReplaceAllString(s, "<TIME>")
	s = shortTimeRegex.ReplaceAllString(s, "<TIME>")
	s = uuidRegex.ReplaceAllString(s, "<UUID>")
	s = ipRegex.ReplaceAllString(s, "<IP>")
	s = hexRegex.ReplaceAllString(s, "<HEX>")
	s = pathRegex.ReplaceAllString(s, "<PATH>")
	s = numRegex.ReplaceAllString(s, "<NUM>")
	return s
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
	IsStackTrace   bool
}

// ClusterLines groups a slice of log lines into template clusters.
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
