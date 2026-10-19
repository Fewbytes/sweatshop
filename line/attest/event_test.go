package attest

import (
	"crypto/ed25519"
	"encoding/base64"
	"reflect"
	"testing"
	"time"
)

func TestCommandEventRoundTripsUnsignedAndKeepsInputTreeSubject(t *testing.T) {
	predicate := CommandEventPredicate{
		Command: "go test ./...", Env: map[string]string{"CI": "1"},
		Capabilities: []string{"linux/amd64", "go"}, Executor: "local",
		ExitCode: 1, OutputBlobs: map[string]string{"stdout": "sha256:aa", "stderr": "sha256:bb"},
		ResultTree: "tree-after",
		StartedAt:  time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 10, 19, 0, 0, 3, 0, time.UTC),
	}
	env, err := UnsignedEventEnvelope(NewCommandEventStatement("tree-before", predicate))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Signatures) != 0 {
		t.Fatalf("event envelope has signatures: %+v", env.Signatures)
	}
	got, err := ReadUnsignedEventEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	if tree, err := got.InputTree(); err != nil || tree != "tree-before" {
		t.Fatalf("InputTree() = %q, %v", tree, err)
	}
	if !reflect.DeepEqual(got.Predicate, predicate) {
		t.Fatalf("predicate did not round-trip:\n got %+v\nwant %+v", got.Predicate, predicate)
	}
}

func TestReadUnsignedEventEnvelopeRejectsChecksStatement(t *testing.T) {
	_, priv := newKey(t)
	env, err := SignStatement(priv, NewStatement("def", "abc", ChecksPredicate{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUnsignedEventEnvelope(env); err == nil {
		t.Fatal("accepted a clean-room check statement as a command event")
	}
}

func TestVerifyStatementRejectsCommandEvent(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, PayloadType, mustMarshalEvent(t))
	if _, _, err := VerifyStatement(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); err == nil {
		t.Fatal("check verification accepted a command event")
	}
}

func TestReadUnsignedEventEnvelopeRejectsMissingInputTree(t *testing.T) {
	statement := NewCommandEventStatement("x", CommandEventPredicate{})
	statement.Subject = nil
	env, err := UnsignedEventEnvelope(statement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUnsignedEventEnvelope(env); err == nil {
		t.Fatal("accepted an event with no input tree subject")
	}
}

func TestReadUnsignedEventEnvelopeRejectsWrongPayloadType(t *testing.T) {
	env, err := UnsignedEventEnvelope(NewCommandEventStatement("x", CommandEventPredicate{}))
	if err != nil {
		t.Fatal(err)
	}
	env.PayloadType = "text/plain"
	if _, err := ReadUnsignedEventEnvelope(env); err == nil {
		t.Fatal("accepted a non-in-toto payload type")
	}
}

func mustMarshalEvent(t *testing.T) []byte {
	t.Helper()
	env, err := UnsignedEventEnvelope(NewCommandEventStatement("x", CommandEventPredicate{}))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
