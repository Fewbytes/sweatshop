package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestPAEMatchesDSSESpecExample(t *testing.T) {
	got := string(PAE("http://example.com/HelloWorld", []byte("hello world")))
	want := "DSSEv1 29 http://example.com/HelloWorld 11 hello world"
	if got != want {
		t.Fatalf("PAE = %q, want %q", got, want)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	payload, keyID, err := Verify(env, map[string]ed25519.PublicKey{KeyID(pub): pub})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "payload" || keyID != KeyID(pub) {
		t.Fatalf("got payload %q key %q", payload, keyID)
	}
}

func TestVerifyRejectsUntrustedKey(t *testing.T) {
	_, priv := newKey(t)
	other, _ := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(other): other}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	env.Payload = base64.StdEncoding.EncodeToString([]byte("forged"))
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}

func TestVerifyRejectsTamperedPayloadType(t *testing.T) {
	pub, priv := newKey(t)
	env := Sign(priv, "text/plain", []byte("payload"))
	env.PayloadType = "application/other"
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(pub): pub}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}

func TestVerifyRejectsSpoofedKeyID(t *testing.T) {
	trustedPub, _ := newKey(t)
	_, attackerPriv := newKey(t)
	env := Sign(attackerPriv, "text/plain", []byte("payload"))
	env.Signatures[0].KeyID = KeyID(trustedPub)
	if _, _, err := Verify(env, map[string]ed25519.PublicKey{KeyID(trustedPub): trustedPub}); !errors.Is(err, ErrNoTrustedSignature) {
		t.Fatalf("err = %v, want ErrNoTrustedSignature", err)
	}
}
