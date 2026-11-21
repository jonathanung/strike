package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Web Push message encryption (RFC 8291, aes128gcm per RFC 8188) and VAPID
// (RFC 8292), standard library only. push_test.go checks the encryption
// against the RFC 8291 Appendix A vectors byte for byte.

const (
	webPushRecordSize = 4096
	vapidTTL          = 12 * time.Hour
	// vapidSubject is the contact the push service sees. It names the
	// software, not the operator.
	vapidSubject = "https://github.com/jonathanung/strike"
)

var b64 = base64.RawURLEncoding

func hmacSHA256(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// encryptWebPush builds an aes128gcm body for one subscription. asPriv and
// salt are parameters so tests can pin them; sendPush passes fresh ones.
func encryptWebPush(uaPublic, authSecret, plaintext []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(authSecret) != 16 {
		return nil, errors.New("web push: auth secret must be 16 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("web push: salt must be 16 bytes")
	}
	uaKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("web push: subscription key: %w", err)
	}
	ecdhSecret, err := asPriv.ECDH(uaKey)
	if err != nil {
		return nil, err
	}
	asPublic := asPriv.PublicKey().Bytes()

	prkKey := hmacSHA256(authSecret, ecdhSecret)
	keyInfo := append([]byte("WebPush: info\x00"), uaPublic...)
	keyInfo = append(keyInfo, asPublic...)
	ikm := hmacSHA256(prkKey, keyInfo, []byte{1})

	prk := hmacSHA256(salt, ikm)
	cek := hmacSHA256(prk, []byte("Content-Encoding: aes128gcm\x00"), []byte{1})[:16]
	nonce := hmacSHA256(prk, []byte("Content-Encoding: nonce\x00"), []byte{1})[:12]

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record: plaintext then the 0x02 last-record delimiter, no padding.
	record := append(append([]byte(nil), plaintext...), 2)
	if len(record)+gcm.Overhead() > webPushRecordSize {
		return nil, errors.New("web push: payload too large")
	}
	header := make([]byte, 0, 16+4+1+len(asPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, webPushRecordSize)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return append(header, gcm.Seal(nil, nonce, record, nil)...), nil
}

// sealWebPush encrypts with a fresh ephemeral key and salt.
func sealWebPush(uaPublic, authSecret, plaintext []byte) ([]byte, error) {
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWebPush(uaPublic, authSecret, plaintext, asPriv, salt)
}

// pushOrigin is the VAPID audience: scheme://host[:port] of the endpoint.
func pushOrigin(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("web push: bad endpoint")
	}
	return u.Scheme + "://" + u.Host, nil
}

// vapidAuthorization returns the Authorization header value for endpoint
// (RFC 8292 "vapid" scheme: an ES256 JWT plus the public key).
func vapidAuthorization(priv *ecdsa.PrivateKey, endpoint string, now time.Time) (string, error) {
	aud, err := pushOrigin(endpoint)
	if err != nil {
		return "", err
	}
	head, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	claims, _ := json.Marshal(map[string]any{
		"aud": aud,
		"exp": now.Add(vapidTTL).Unix(),
		"sub": vapidSubject,
	})
	signing := b64.EncodeToString(head) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + b64.EncodeToString(pub), nil
}

func newVAPIDKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// decodeSubscriptionKeys validates the browser's p256dh (an uncompressed
// P-256 point) and auth (16 bytes) values.
func decodeSubscriptionKeys(p256dh, auth string) ([]byte, []byte, error) {
	pub, err := decodeB64Any(p256dh)
	if err != nil || len(pub) != 65 || pub[0] != 4 {
		return nil, nil, errors.New("invalid p256dh key")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return nil, nil, errors.New("invalid p256dh key")
	}
	sec, err := decodeB64Any(auth)
	if err != nil || len(sec) != 16 {
		return nil, nil, errors.New("invalid auth secret")
	}
	return pub, sec, nil
}

// decodeB64Any accepts base64url with or without padding (browsers differ).
func decodeB64Any(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	return b64.DecodeString(s)
}
