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
