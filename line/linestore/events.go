package linestore

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// EventKind says what happened on the line.
type EventKind string

const (
	EventOffer      EventKind = "offer"
	EventClaim      EventKind = "claim"
	EventRelease    EventKind = "release"
	EventLabel      EventKind = "label"
	EventGiveUp     EventKind = "give-up"
	EventClose      EventKind = "close"
	EventReap       EventKind = "reap"
	EventSubmit     EventKind = "submit"
	EventMerge      EventKind = "merge"
	EventAndonPull  EventKind = "andon-pull"
	EventAndonClear EventKind = "andon-clear"
	// EventAcceptanceRequest and EventAcceptanceAnswer are the track record of
	// requests to change acceptance tests and Gemba's answers to them.
	EventAcceptanceRequest EventKind = "acceptance-request"
	EventAcceptanceAnswer  EventKind = "acceptance-answer"
)

// MaxEvents bounds one Events call: the newest MaxEvents matching events.
const MaxEvents = 1000

// eventTimeFormat is RFC 3339 UTC with fixed nanosecond width, unlike
// RFC3339Nano, so that stored times order the same as text and as time.
const eventTimeFormat = "2006-01-02T15:04:05.000000000Z"

// Event is one line event. Card and Station are empty when the event has none
// (a submission names a commit, in Detail, not a card).
type Event struct {
	At      time.Time `json:"at"`
	Kind    EventKind `json:"kind"`
	Card    string    `json:"card,omitempty"`
	Station string    `json:"station,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// EventFilter selects events; zero fields do not filter. Since is inclusive.
type EventFilter struct {
	Since         time.Time
	Card, Station string
}

// RecordEvent appends e to the event log.
func (s *Store) RecordEvent(ctx context.Context, e Event) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO line_event (at, kind, card, station, detail) VALUES (?, ?, ?, ?, ?)`,
		e.At.UTC().Format(eventTimeFormat), string(e.Kind), e.Card, e.Station, e.Detail)
	return err
}

// Events returns the newest MaxEvents events matching f, oldest first.
func (s *Store) Events(ctx context.Context, f EventFilter) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, kind, card, station, detail FROM line_event
		 WHERE at >= ? AND (? = '' OR card = ?) AND (? = '' OR station = ?)
		 ORDER BY seq DESC LIMIT ?`,
		f.Since.UTC().Format(eventTimeFormat), f.Card, f.Card, f.Station, f.Station, MaxEvents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at, kind string
		if err := rows.Scan(&at, &kind, &e.Card, &e.Station, &e.Detail); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(eventTimeFormat, at); err != nil {
			return nil, fmt.Errorf("stored event time %q: %w", at, err)
		}
		e.Kind = EventKind(kind)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}
