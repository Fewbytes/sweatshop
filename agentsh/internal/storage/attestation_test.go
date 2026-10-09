package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAttestationRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	passed := Attestation{ID: "att_1", Commit: "c1", Tree: "t1", Passed: true, Statement: []byte(`{"s":1}`), Envelope: []byte(`{"e":1}`), CreatedAt: created}
	failed := Attestation{ID: "att_2", Commit: "c2", Tree: "t2", Passed: false, Statement: []byte(`{"s":2}`), CreatedAt: created}
	for _, a := range []Attestation{passed, failed} {
		if err := store.SaveAttestation(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetAttestation(ctx, "att_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != "c1" || !got.Passed || string(got.Envelope) != `{"e":1}` || !got.CreatedAt.Equal(created) {
		t.Fatalf("got %+v", got)
	}
	got2, err := store.GetAttestation(ctx, "att_2")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Passed || got2.Envelope != nil {
		t.Fatalf("failed attestation round-tripped as %+v", got2)
	}
	if _, err := store.GetAttestation(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing attestation err = %v", err)
	}
}
