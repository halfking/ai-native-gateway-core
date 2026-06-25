package identity

import (
	"net/http"
	"strings"
	"testing"
)

func TestShortID_16Plus(t *testing.T) {
	c := &ClientIdentity{IdentityHash: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}
	if got := c.ShortID(); got != "abcdef0123456789" {
		t.Fatalf("ShortID() = %q, want abcdef0123456789", got)
	}
}

func TestShortID_Short(t *testing.T) {
	c := &ClientIdentity{IdentityHash: "abc"}
	if got := c.ShortID(); got != "abc" {
		t.Fatalf("ShortID() = %q, want abc", got)
	}
}

func TestShortID_Empty(t *testing.T) {
	c := &ClientIdentity{}
	if got := c.ShortID(); got != "" {
		t.Fatalf("ShortID() = %q, want empty string", got)
	}
}

func TestExtractFingerprint_WithSeedHeaders(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(headerDeviceSeed, "d1")
	r.Header.Set(headerMachineID, "m1")
	r.Header.Set(headerRuntimeName, "go")
	r.Header.Set(headerRuntimeVer, "1.26")
	r.Header.Set(headerOSName, "darwin")
	r.Header.Set(headerOSArch, "arm64")
	r.Header.Set("User-Agent", "test/1.0")

	fp := ExtractFingerprint(r, "roocode")
	if fp.DeviceSeed != "d1" || fp.MachineID != "m1" {
		t.Fatal("seed/machine ID headers not extracted")
	}
	if fp.UserAgent != "test/1.0" {
		t.Fatalf("UserAgent = %q, want test/1.0", fp.UserAgent)
	}
	if fp.ClientProfile != "roocode" {
		t.Fatalf("ClientProfile = %q, want roocode", fp.ClientProfile)
	}
}

func TestExtractFingerprint_NoSeedButClientIP(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.RemoteAddr = "203.0.113.5:1234"

	fp := ExtractFingerprint(r, "p")
	if !strings.HasPrefix(fp.UserAgent, "ip:203.0.113.5") {
		t.Fatalf("UserAgent should be prefixed with IP, got %q", fp.UserAgent)
	}
}

func TestExtractFingerprint_NoSeedNoIP(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	fp := ExtractFingerprint(r, "p")
	if fp.UserAgent != "ip:unknown" {
		t.Fatalf("UserAgent should default to ip:unknown, got %q", fp.UserAgent)
	}
}

func TestExtractFingerprint_XForwardedFor(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	r.RemoteAddr = "10.0.0.1:9999"

	fp := ExtractFingerprint(r, "p")
	if !strings.HasPrefix(fp.UserAgent, "ip:198.51.100.7") {
		t.Fatalf("X-Forwarded-For first IP should be used, got %q", fp.UserAgent)
	}
}

func TestExtractClientIP_XForwardedForSingle(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	r.RemoteAddr = "10.0.0.1:9999"
	if got := extractClientIP(r); got != "203.0.113.10" {
		t.Fatalf("extractClientIP = %q, want 203.0.113.10", got)
	}
}

func TestExtractClientIP_RemoteAddr(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:8080"
	if got := extractClientIP(r); got != "10.0.0.5" {
		t.Fatalf("extractClientIP = %q, want 10.0.0.5", got)
	}
}

func TestExtractClientIP_Empty(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	if got := extractClientIP(r); got != "" {
		t.Fatalf("extractClientIP = %q, want empty", got)
	}
}

func TestHexBytes_OddLengthTruncation(t *testing.T) {
	// 3 chars → odd → truncated to 2 chars → 1 byte
	got := hexBytes("abc", 0, 3)
	if len(got) != 1 {
		t.Fatalf("hexBytes odd length should truncate: got len %d, want 1", len(got))
	}
}

func TestHexBytes_EndExceedsLen(t *testing.T) {
	// 2 chars, end=100 → end capped to 2 → even length 2 → 1 byte
	got := hexBytes("ab", 0, 100)
	if len(got) != 1 {
		t.Fatalf("hexBytes end > len should cap: got len %d, want 1", len(got))
	}
}

func TestHexBytes_StartAtEnd(t *testing.T) {
	// start >= len(s) → return nil
	if got := hexBytes("ab", 5, 10); got != nil {
		t.Fatalf("hexBytes start>=len should return nil, got %v", got)
	}
}

func TestHexBytes_Empty(t *testing.T) {
	if got := hexBytes("", 0, 0); got != nil {
		t.Fatalf("hexBytes empty input should return nil, got %v", got)
	}
}

func TestDeriveVirtualIP_Fallback(t *testing.T) {
	got := deriveVirtualIP("")
	if got != "192.0.2.1" {
		t.Fatalf("deriveVirtualIP(\"\") = %q, want 192.0.2.1", got)
	}
}

func TestDeriveVirtualMAC_Fallback(t *testing.T) {
	got := deriveVirtualMAC("")
	if got != "02:00:00:00:00:00" {
		t.Fatalf("deriveVirtualMAC(\"\") = %q, want 02:00:00:00:00:00", got)
	}
}

func TestBuildIdentity_AppIDTakesPrecedence(t *testing.T) {
	appID := 1
	keyID := 99
	id := BuildIdentity("t1", &appID, &keyID, ClientFingerprint{DeviceSeed: "d"})
	if id.AppOrKey != "app1" {
		t.Fatalf("applicationID should take precedence, got AppOrKey=%q", id.AppOrKey)
	}
}

func TestBuildIdentity_KeyIDWhenNoAppID(t *testing.T) {
	keyID := 99
	id := BuildIdentity("t1", nil, &keyID, ClientFingerprint{DeviceSeed: "d"})
	if id.AppOrKey != "key99" {
		t.Fatalf("apiKeyID should be used when applicationID is nil, got AppOrKey=%q", id.AppOrKey)
	}
}

func TestBuildIdentity_NoAnchors(t *testing.T) {
	id := BuildIdentity("t1", nil, nil, ClientFingerprint{DeviceSeed: "d"})
	if id.AppOrKey != "key0" {
		t.Fatalf("no anchors should default to key0, got %q", id.AppOrKey)
	}
}

func TestBuildIdentity_LogSourceMachineID(t *testing.T) {
	id := BuildIdentity("t1", nil, nil, ClientFingerprint{MachineID: "m"})
	if id.IdentityHash == "" {
		t.Fatal("identity hash should be set when fingerprint uses machine ID")
	}
}
