package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	maxSkewPast   = 5 * time.Minute
	maxSkewFuture = 60 * time.Second
)

var (
	errBadSignature = errors.New("signature rejected")
	errStale        = errors.New("timestamp outside the replay window")
	errMalformed    = errors.New("malformed signature headers")
)

// SignWebhook is the X-Strike-Signature value over timestamp + "." + body.
func SignWebhook(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// SignGitHub is the X-Hub-Signature-256 value over the raw body.
func SignGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func verifyWebhook(secret, timestamp string, body []byte, sig string, now time.Time) error {
	ts := strings.TrimSpace(timestamp)
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || ts == "" {
		_ = macEqual(secret, signedWebhookPayload("0", body), sig)
		return errMalformed
	}
	when := time.Unix(sec, 0)
	if now.Sub(when) > maxSkewPast || when.Sub(now) > maxSkewFuture {
		// Still compare so a stale timestamp does not skip the MAC.
		_ = macEqual(secret, signedWebhookPayload(ts, body), sig)
		return errStale
	}
	if !macEqual(secret, signedWebhookPayload(ts, body), sig) {
		return errBadSignature
	}
	return nil
}

func verifyGitHub(secret string, body []byte, sig string) error {
	if !macEqual(secret, body, sig) {
		return errBadSignature
	}
	return nil
}

func signedWebhookPayload(timestamp string, body []byte) []byte {
	out := make([]byte, 0, len(timestamp)+1+len(body))
	out = append(out, timestamp...)
	out = append(out, '.')
	out = append(out, body...)
	return out
}

// macEqual compares HMAC-SHA256(secret, payload) to a "sha256=<hex>" header.
// A bad or short header still runs a compare so the reject path stays flat.
func macEqual(secret string, payload []byte, header string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	want := mac.Sum(nil)
	got, ok := decodeSig(header)
	if !ok {
		blank := make([]byte, len(want))
		hmac.Equal(want, blank)
		return false
	}
	return hmac.Equal(want, got)
}

func decodeSig(header string) ([]byte, bool) {
	header = strings.TrimSpace(header)
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return nil, false
	}
	raw, err := hex.DecodeString(header[len(prefix):])
	if err != nil || len(raw) != sha256.Size {
		return nil, false
	}
	return raw, true
}
