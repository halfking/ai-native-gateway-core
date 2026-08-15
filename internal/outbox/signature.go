package outbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const SignatureScheme = "hmac-sha256="

// SignPayload signs timestamp + "." + nonce + "." + body using HMAC-SHA256.
func SignPayload(secret, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write([]byte(nonce))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return SignatureScheme + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature verifies the contract signature in constant time.
func VerifySignature(secret, timestamp, nonce string, body []byte, provided string) bool {
	if !strings.HasPrefix(provided, SignatureScheme) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(provided, SignatureScheme))
	if err != nil {
		return false
	}
	wantHex := strings.TrimPrefix(SignPayload(secret, timestamp, nonce, body), SignatureScheme)
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

// computeHMAC and VerifyHMAC retain the legacy body-only helpers for callers
// outside the delivery path. Webhook delivery must use SignPayload instead.
func computeHMAC(data []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyHMAC(data []byte, secret, providedSignature string) bool {
	expected := computeHMAC(data, secret)
	return hmac.Equal([]byte(expected), []byte(providedSignature))
}
