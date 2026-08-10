package outbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// computeHMAC computes HMAC-SHA256 signature for the given payload.
//
// Contract requirement (02-CROSS-REPO-EVENT-CONTRACT.md §4):
//   - Algorithm: HMAC-SHA256
//   - Input: JSON bytes of the complete event envelope
//   - Output: hex-encoded signature string
//
// Usage:
//
//	signature := computeHMAC(envelopeJSON, sharedSecret)
//	req.Header.Set("X-Event-Signature", signature)
func computeHMAC(data []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyHMAC verifies that the provided signature matches the computed HMAC for data.
//
// Returns true if signature is valid, false otherwise.
// Used by ASM to verify Gateway's event signatures.
func VerifyHMAC(data []byte, secret string, providedSignature string) bool {
	expectedSignature := computeHMAC(data, secret)
	return hmac.Equal([]byte(expectedSignature), []byte(providedSignature))
}
