package executor

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/avishai-ish-shalom/sweatshop/agentsh/internal/storage"
)

const (
	DefaultLoopThreshold = 3
	DefaultLoopWindow    = 5 * time.Minute
)

// LoopConfig configures passive detection of repeated failing invocations.
type LoopConfig struct {
	Threshold int           // Number of equivalent failures to trigger warning (default: 3)
	Window    time.Duration // Time window within which failures must occur (default: 5m)
}

// DefaultLoopConfig returns the default loop detection settings.
func DefaultLoopConfig() LoopConfig {
	return LoopConfig{
		Threshold: DefaultLoopThreshold,
		Window:    DefaultLoopWindow,
	}
}

type failureRecord struct {
	id         string
	normalized string
	timestamp  time.Time
}

// LoopDetector passively tracks invocations per session to detect when near-identical
// failing commands are repeated in a loop.
type LoopDetector struct {
	mu       sync.Mutex
	config   LoopConfig
	failures map[string][]failureRecord // session -> failure records
}

// NewLoopDetector creates a loop detector with the given configuration.
func NewLoopDetector(config LoopConfig) *LoopDetector {
	if config.Threshold <= 0 {
		config.Threshold = DefaultLoopThreshold
	}
	if config.Window <= 0 {
		config.Window = DefaultLoopWindow
	}
	return &LoopDetector{
		config:   config,
		failures: make(map[string][]failureRecord),
	}
}

// RecordAndCheck records a failed invocation and returns a warning string if
// repeated equivalent failures reach the configured threshold.
func (d *LoopDetector) RecordAndCheck(inv storage.Invocation) string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	sessionName := inv.Session
	if sessionName == "" {
		sessionName = "default"
	}

	now := time.Now().UTC()
	if inv.EndedAt != nil {
		now = *inv.EndedAt
	}

	norm := normalizeCommand(inv.Command)
	if norm == "" {
		return ""
	}

	// Prune old records outside window
	cutoff := now.Add(-d.config.Window)
	existing := d.failures[sessionName]
	valid := make([]failureRecord, 0, len(existing)+1)
	for _, rec := range existing {
		if !rec.timestamp.Before(cutoff) {
			valid = append(valid, rec)
		}
	}

	// Find prior matching failures
	var priorIDs []string
	for _, rec := range valid {
		if rec.normalized == norm {
			priorIDs = append(priorIDs, rec.id)
		}
	}

	// Add current failure
	valid = append(valid, failureRecord{
		id:         inv.ID,
		normalized: norm,
		timestamp:  now,
	})
	d.failures[sessionName] = valid

	// Total failures for this command in window (including current)
	count := len(priorIDs) + 1
	if count >= d.config.Threshold {
		var idList string
		if len(priorIDs) > 0 {
			// Limit displayed prior IDs to at most 5 to stay bounded
			shownIDs := priorIDs
			if len(shownIDs) > 5 {
				shownIDs = shownIDs[len(shownIDs)-5:]
			}
			idList = fmt.Sprintf(" (prior: %s)", strings.Join(shownIDs, ", "))
		}
		return fmt.Sprintf("[loop detected — equivalent command failed %d times in %s%s]\n→ check error output or change approach instead of repeating the same command",
			count, formatDuration(d.config.Window), idList)
	}

	return ""
}

// RecordSuccess clears prior failures for the matching command upon success.
func (d *LoopDetector) RecordSuccess(inv storage.Invocation) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	sessionName := inv.Session
	if sessionName == "" {
		sessionName = "default"
	}

	norm := normalizeCommand(inv.Command)
	if norm == "" {
		return
	}

	existing := d.failures[sessionName]
	if len(existing) == 0 {
		return
	}

	var remaining []failureRecord
	for _, rec := range existing {
		if rec.normalized != norm {
			remaining = append(remaining, rec)
		}
	}
	if len(remaining) == 0 {
		delete(d.failures, sessionName)
	} else {
		d.failures[sessionName] = remaining
	}
}

// normalizeCommand collapses whitespace, trims trailing semicolons and whitespace
// so trivial differences (e.g. extra spaces) are treated as equivalent.
func normalizeCommand(cmd string) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	joined := strings.Join(fields, " ")
	return strings.TrimRight(joined, "; ")
}

func formatDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return d.String()
}
