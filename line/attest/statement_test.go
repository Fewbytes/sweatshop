package attest

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestStatementSignVerifyRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	statement := NewStatement("abc123", ChecksPredicate{
		Commit: "def456", Role: "maker", AgentID: "maker-1", Constitution: "sha256:x", Runner: "local",
		Checks:     []CheckResult{{Name: "ci", Command: "just ci", ExitCode: 0, EvidenceID: "inv_1"}},
		FinishedAt: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
	})
	env, err := SignStatement(priv, statement)
	if err != nil {
		t.Fatal(err)
	}
	got, keyID, err := VerifyStatement(env, map[string]ed25519.PublicKey{KeyID(pub): pub})
	if err != nil {
		t.Fatal(err)
	}
	if keyID != KeyID(pub) {
		t.Fatalf("keyID = %s", keyID)
	}
	tree, err := got.Tree()
	if err != nil || tree != "abc123" {
		t.Fatalf("Tree() = %q, %v", tree, err)
	}
	if got.Predicate.Checks[0].EvidenceID != "inv_1" || got.Predicate.Role != "maker" {
		t.Fatalf("predicate did not round-trip: %+v", got.Predicate)
	}
}

func TestVerifyStatementRejectsWrongPayloadType(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte(`{}`))
	if _, _, err := VerifyStatement(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); err == nil {
		t.Fatal("accepted a non-in-toto payload type")
	}
}

func TestVerifyStatementRejectsForeignPredicate(t *testing.T) {
	pub, priv := newKey(t)
	statement := NewStatement("abc", ChecksPredicate{})
	statement.PredicateType = "https://slsa.dev/provenance/v1"
	env, err := SignStatement(priv, statement)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyStatement(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); err == nil {
		t.Fatal("accepted a statement with a foreign predicate type")
	}
}

func TestTreeRequiresGitTreeSubject(t *testing.T) {
	statement := Statement{Subject: []Subject{{Name: "file", Digest: map[string]string{"sha256": "x"}}}}
	if _, err := statement.Tree(); err == nil {
		t.Fatal("Tree() found a tree in a statement without one")
	}
}
