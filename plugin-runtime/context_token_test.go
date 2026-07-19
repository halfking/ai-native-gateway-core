package pluginruntime

import (
	"testing"
	"time"
)

func TestSignContext_RoundTrip(t *testing.T) {
	secret := []byte("s")
	req := SignContextRequest(secret, "ai-session-manager", "t-1", time.Now().Unix(), "n")
	if req.Header.Get("X-Gateway-Context-Signature") == "" {
		t.Fatal("missing signature header")
	}
	if req.Header.Get("X-Gateway-Plugin-ID") != "ai-session-manager" {
		t.Fatal("missing plugin id header")
	}
}

func TestSignContext_GoldenVector(t *testing.T) {
	// Pinned vector: if this fails, the gateway/plugin HMAC contract drifted.
	// Cross-check: ai-session-manager/internal/plugin/context.go must produce the same hex
	// for the same inputs.
	secret := []byte("golden-secret")
	req := SignContextRequest(secret, "ai-session-manager", "tenant-42", 1700000000, "nonce-7")
	got := req.Header.Get("X-Gateway-Context-Signature")
	// EXPECTED verified via openssl cross-check:
	//   echo -n 'ai-session-manager|tenant-42|1700000000|nonce-7' \
	//     | openssl dgst -sha256 -hmac 'golden-secret'
	const expected = "ef8faf98acc072617b49d8e341e9f8f6ec2c292522377ea090524a9b0b57b516"
	if got != expected {
		t.Fatalf("signature drifted: got %s want %s", got, expected)
	}
}
