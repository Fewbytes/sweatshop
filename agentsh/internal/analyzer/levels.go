package analyzer

import "strings"

// DetectLogLevel extracts the log level from a log line if recognizable.
func DetectLogLevel(line string) string {
	upper := strings.ToUpper(line)
	// Check in rough order of severity
	switch {
	case strings.Contains(upper, "PANIC") || strings.Contains(upper, "FATAL") || strings.Contains(upper, "CRITICAL"):
		return "FATAL"
	case strings.Contains(upper, "ERROR") || strings.Contains(upper, "[ERR]") || strings.Contains(upper, "LEVEL=ERROR") || strings.Contains(upper, "\"LEVEL\":\"ERROR\""):
		return "ERROR"
	case strings.Contains(upper, "WARN") || strings.Contains(upper, "WARNING") || strings.Contains(upper, "[WRN]") || strings.Contains(upper, "LEVEL=WARN") || strings.Contains(upper, "\"LEVEL\":\"WARN\""):
		return "WARN"
	case strings.Contains(upper, "INFO") || strings.Contains(upper, "[INF]") || strings.Contains(upper, "LEVEL=INFO") || strings.Contains(upper, "\"LEVEL\":\"INFO\""):
		return "INFO"
	case strings.Contains(upper, "DEBUG") || strings.Contains(upper, "TRACE") || strings.Contains(upper, "[DBG]"):
		return "DEBUG"
	default:
		return ""
	}
}

// ComputeLevelHistogram builds a count of detected log levels across a slice of lines.
func ComputeLevelHistogram(lines []string) map[string]int {
	counts := make(map[string]int)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if lvl := DetectLogLevel(trimmed); lvl != "" {
			counts[lvl]++
		}
	}
	return counts
}
