package licensing

import "testing"

func TestMaskBootstrapLicenseKey(t *testing.T) {
	if got := maskBootstrapLicenseKey("abcd"); got != "abcd" {
		t.Fatalf("short key: got %q", got)
	}
	got := maskBootstrapLicenseKey("ABCDEFGH12345678")
	if got != "ABCD••••5678" {
		t.Fatalf("masked: got %q", got)
	}
}

func TestMapBodyString(t *testing.T) {
	if mapBodyString(nil, "x") != "" {
		t.Fatal("nil body")
	}
	if mapBodyString(map[string]any{"signed_license": "abc"}, "signed_license") != "abc" {
		t.Fatal("string value")
	}
	if mapBodyString(map[string]any{"signed_license": 1}, "signed_license") != "" {
		t.Fatal("non-string ignored")
	}
}

func TestResolveCenterURLEmpty(t *testing.T) {
	// Without env, empty is OK (center unreachable → activate-quick returns 503).
	_ = resolveCenterURL()
}
