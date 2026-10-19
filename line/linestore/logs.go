package linestore

import (
	"context"
	"fmt"
	"slices"
	"time"
)

const (
	// MaxLogs bounds one Logs call: the newest MaxLogs matching lines.
	MaxLogs = 1000
	// MaxStoredLogs bounds the table; older lines are deleted as new ones arrive.
	MaxStoredLogs = 20000
	// MaxLogLineBytes bounds one stored line; longer lines are cut.
	MaxLogLineBytes = 2000
)

// LogLine is one line a line component logged.
type LogLine struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Line   string    `json:"line"`
}

// LogFilter selects log lines; zero fields do not filter. Since is inclusive.
type LogFilter struct {
	Since  time.Time
	Source string
}

// RecordLog appends l, cutting its line to MaxLogLineBytes, and deletes the
// lines beyond the newest MaxStoredLogs. The caller redacts first.
func (s *Store) RecordLog(ctx context.Context, l LogLine) error {
	if len(l.Line) > MaxLogLineBytes {
		l.Line = l.Line[:MaxLogLineBytes] + "…[cut]"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO component_log (at, source, line) VALUES (?, ?, ?)`,
		l.At.UTC().Format(eventTimeFormat), l.Source, l.Line)
	if err != nil {
		return err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM component_log WHERE seq <= ?`, seq-MaxStoredLogs)
	return err
}

// Logs returns the newest MaxLogs lines matching f, oldest first.
func (s *Store) Logs(ctx context.Context, f LogFilter) ([]LogLine, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, source, line FROM component_log
		 WHERE at >= ? AND (? = '' OR source = ?)
		 ORDER BY seq DESC LIMIT ?`,
		f.Since.UTC().Format(eventTimeFormat), f.Source, f.Source, MaxLogs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var l LogLine
		var at string
		if err := rows.Scan(&at, &l.Source, &l.Line); err != nil {
			return nil, err
		}
		if l.At, err = time.Parse(eventTimeFormat, at); err != nil {
			return nil, fmt.Errorf("stored log time %q: %w", at, err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}
