// Package attest signs and verifies DSSE envelopes carrying in-toto
// statements with Ed25519 keys.
package attest

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

var ErrNoTrustedSignature = errors.New("no valid signature from a trusted key")

// PAE is the DSSE pre-authentication encoding. Signatures cover it rather than
// the raw payload, so the payload type cannot be swapped under a signature.
func PAE(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}

func Sign(key ed25519.PrivateKey, payloadType string, payload []byte) Envelope {
	pub := key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(key, PAE(payloadType, payload))
	return Envelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(sig)}},
	}
}

// Verify returns the payload and the ID of the first trusted key whose
// signature checks out.
func Verify(env Envelope, trusted map[string]ed25519.PublicKey) ([]byte, string, error) {
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, "", fmt.Errorf("decode payload: %w", err)
	}
	message := PAE(env.PayloadType, payload)
	for _, s := range env.Signatures {
		pub, ok := trusted[s.KeyID]
		if !ok {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			continue
		}
		if ed25519.Verify(pub, message, sig) {
			return payload, s.KeyID, nil
		}
	}
	return nil, "", ErrNoTrustedSignature
}
