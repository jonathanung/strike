package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

type pkceCodes struct {
	verifier  string
	challenge string
}

func newPKCE() pkceCodes {
	verifier := randomURLSafe(64)
	sum := sha256.Sum256([]byte(verifier))
	return pkceCodes{
		verifier:  verifier,
		challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}
}

func randomURLSafe(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)[:nBytes]
}
