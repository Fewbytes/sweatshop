package linestore

import (
	"context"
	"fmt"
	"time"
)

// TokenUsage is one report of tokens a station spent working Card.
type TokenUsage struct {
	At      time.Time `json:"at"`
	Station string    `json:"station"`
	Role    string    `json:"role"`
	Card    string    `json:"card"`
	Tokens  int64     `json:"tokens"`
}

// RecordTokenUsage appends u to the token usage log.
func (s *Store) RecordTokenUsage(ctx context.Context, u TokenUsage) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO token_usage (at, station, role, card, tokens) VALUES (?, ?, ?, ?, ?)`,
		u.At.UTC().Format(eventTimeFormat), u.Station, u.Role, u.Card, u.Tokens)
	return err
}

// TokenUsageSince returns every report made at or after since, oldest first.
func (s *Store) TokenUsageSince(ctx context.Context, since time.Time) ([]TokenUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, station, role, card, tokens FROM token_usage WHERE at >= ? ORDER BY seq`,
		since.UTC().Format(eventTimeFormat))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenUsage{}
	for rows.Next() {
		var u TokenUsage
		var at string
		if err := rows.Scan(&at, &u.Station, &u.Role, &u.Card, &u.Tokens); err != nil {
			return nil, err
		}
		if u.At, err = time.Parse(eventTimeFormat, at); err != nil {
			return nil, fmt.Errorf("stored token usage time %q: %w", at, err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
