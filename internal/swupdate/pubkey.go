package swupdate

import (
	"crypto/ed25519"
	"encoding/base64"
)

// publicKeyB64: öffentlicher Ed25519-Schlüssel der Releases (aus `relsign genkey`).
// Der zugehörige private Schlüssel liegt nur beim Herausgeber.
const publicKeyB64 = "csEFfngznvjfLfofIVh/oP8HqW9kJWa6uIFp8X2Gs9w="

// PublicKey liefert den eingebauten Prüfschlüssel (nil, wenn ungültig).
func PublicKey() ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(b)
}
