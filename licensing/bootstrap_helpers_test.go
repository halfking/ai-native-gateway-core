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

func TestResolveCenterURLPriorityAndFallbacks(t *testing.T) {
	for _, key := range []string{
		"LLM_GATEWAY_CENTER_URL",
		"LICENSE_AUTHORITY_URL",
		"MAINTAIN_SERVICE_URL",
		"OPS_COLLECT_URL",
	} {
		t.Setenv(key, "")
	}

	t.Setenv("OPS_COLLECT_URL", "https://ops.example.test///")
	if got := resolveCenterURL(); got != "https://ops.example.test" {
		t.Fatalf("OPS_COLLECT_URL fallback: got %q", got)
	}

	t.Setenv("MAINTAIN_SERVICE_URL", "https://maintain.example.test/")
	if got := resolveCenterURL(); got != "https://maintain.example.test" {
		t.Fatalf("MAINTAIN_SERVICE_URL fallback: got %q", got)
	}

	t.Setenv("LICENSE_AUTHORITY_URL", "https://authority.example.test/")
	if got := resolveCenterURL(); got != "https://authority.example.test" {
		t.Fatalf("LICENSE_AUTHORITY_URL priority: got %q", got)
	}

	t.Setenv("LLM_GATEWAY_CENTER_URL", "https://center.example.test/")
	if got := resolveCenterURL(); got != "https://center.example.test" {
		t.Fatalf("LLM_GATEWAY_CENTER_URL priority: got %q", got)
	}
}

func TestResolveCenterURLDefault(t *testing.T) {
	for _, key := range []string{
		"LLM_GATEWAY_CENTER_URL",
		"LICENSE_AUTHORITY_URL",
		"MAINTAIN_SERVICE_URL",
		"OPS_COLLECT_URL",
	} {
		t.Setenv(key, "")
	}
	if got := resolveCenterURL(); got != defaultCenterURL {
		t.Fatalf("default center URL: got %q, want %q", got, defaultCenterURL)
	}
}
