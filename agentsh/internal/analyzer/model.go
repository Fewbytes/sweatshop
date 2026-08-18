package analyzer

// LogTemplate represents an aggregated cluster of structurally equivalent log lines.
type LogTemplate struct {
	ID             string `json:"id"`
	Template       string `json:"template"`
	Count          int    `json:"count"`
	FirstLine      int    `json:"first_line"`
	LastLine       int    `json:"last_line"`
	ExemplarOffset int64  `json:"exemplar_offset"`
	Exemplar       string `json:"exemplar"`
	Level          string `json:"level,omitempty"`
	IsStackTrace   bool   `json:"is_stack_trace,omitempty"`
	Novel          bool   `json:"novel,omitempty"`
	PriorCount     int    `json:"prior_count,omitempty"`
}

// LogAnalysis holds the complete structured analysis of a log stream.
type LogAnalysis struct {
	InvocationID string         `json:"invocation_id"`
	Stream       string         `json:"stream"`
	TotalLines   int            `json:"total_lines"`
	Templates    []LogTemplate  `json:"templates"`
	Levels       map[string]int `json:"levels"`
	Summary      string         `json:"summary,omitempty"`
	// Truncated is true when the source stream exceeded the analysis byte cap
	// and trailing content was not scanned.
	Truncated bool `json:"truncated,omitempty"`
}
