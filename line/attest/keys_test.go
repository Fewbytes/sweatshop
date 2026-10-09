package attest

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndLoadKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	pub, err := GenerateKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("loaded key does not match generated public key")
	}
	encoded, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePublicKey(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(pub) {
		t.Fatal(".pub file does not match generated key")
	}
}

func TestGenerateKeyFileNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	if _, err := GenerateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateKeyFile(path); err == nil {
		t.Fatal("second GenerateKeyFile overwrote an existing key")
	}
}

func TestLoadPrivateKeyRejectsReadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.key")
	if _, err := GenerateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("loaded a world-readable signing key")
	}
}

func TestParsePublicKeyRejectsWrongLength(t *testing.T) {
	if _, err := ParsePublicKey("AAAA"); err == nil {
		t.Fatal("accepted a 3-byte public key")
	}
}
