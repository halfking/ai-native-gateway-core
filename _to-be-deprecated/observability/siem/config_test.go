
package siem

import (
	"strings"
	"testing"
)

// TestDefaults_AreSafe verifies the platform default is non-leaky:
// disabled + local file path, not a remote URL.
func TestDefaults_AreSafe(t *testing.T) {
	c := Defaults()
	if c.Enabled {
		t.Error("default config must NOT be enabled (operator must consciously enable)")
	}
	if c.IsRemote() {
		t.Errorf("default endpoint %q must NOT be remote", c.Endpoint)
	}
	if c.Format != WireCEF {
		t.Errorf("default format = %q, want %q", c.Format, WireCEF)
	}
	if c.BatchSize <= 0 || c.BatchSize > 1000 {
		t.Errorf("BatchSize out of [1,1000]: %d", c.BatchSize)
	}
	if c.FlushMs < 100 || c.FlushMs > 60000 {
		t.Errorf("FlushMs out of [100,60000]: %d", c.FlushMs)
	}
}

// TestLoad_Empty_ReturnsDefaults verifies Load("") gives Defaults().
func TestLoad_Empty_ReturnsDefaults(t *testing.T) {
	got, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	// Compare scalar fields; ExtraFields (map) is nil on both sides, OK.
	if got.Enabled != want.Enabled ||
		got.Endpoint != want.Endpoint ||
		got.Format != want.Format ||
		got.BatchSize != want.BatchSize ||
		got.FlushMs != want.FlushMs ||
		got.Retries != want.Retries {
		t.Errorf("Load(\"\") = %+v, want Defaults() (scalar mismatch)", got)
	}
}

// TestLoad_ValidJSON verifies a complete config round-trips.
func TestLoad_ValidJSON(t *testing.T) {
	raw := `{"enabled":true,"endpoint":"https://splunk.example.com/services/collector","format":"cef","batch_size":50,"flush_ms":2000,"retries":5,"extra_fields":{"environment":"prod"}}`
	c, err := Load(raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Enabled {
		t.Error("Enabled should be true")
	}
	if c.BatchSize != 50 {
		t.Errorf("BatchSize = %d, want 50", c.BatchSize)
	}
	if c.ExtraFields["environment"] != "prod" {
		t.Errorf("ExtraFields[environment] = %q, want prod", c.ExtraFields["environment"])
	}
}

// TestLoad_PartialJSON_FillsDefaults verifies missing fields are filled.
func TestLoad_PartialJSON_FillsDefaults(t *testing.T) {
	c, err := Load(`{"enabled":true,"endpoint":"/tmp/siem.log"}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Format != WireCEF {
		t.Errorf("Format should default to WireCEF, got %q", c.Format)
	}
	if c.BatchSize != 100 {
		t.Errorf("BatchSize should default to 100, got %d", c.BatchSize)
	}
	if c.FlushMs != 5000 {
		t.Errorf("FlushMs should default to 5000, got %d", c.FlushMs)
	}
}

// TestLoad_MalformedJSON_ReturnsDefaults verifies Load on garbage returns
// Defaults + error (caller can log + continue, never crash).
func TestLoad_MalformedJSON_ReturnsDefaults(t *testing.T) {
	got, err := Load("not json at all")
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	want := Defaults()
	if got.Enabled != want.Enabled ||
		got.Endpoint != want.Endpoint ||
		got.Format != want.Format ||
		got.BatchSize != want.BatchSize {
		t.Errorf("got %+v, want Defaults() on parse error", got)
	}
}

// TestLoad_RejectsHTTPPlaintext rejects http:// endpoints (would leak CEF
// over plaintext). Operators must use https://.
func TestLoad_RejectsHTTPPlaintext(t *testing.T) {
	_, err := Load(`{"endpoint":"http://splunk.example.com/collect"}`)
	if err == nil {
		t.Fatal("expected error for http:// endpoint")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error should mention https, got: %v", err)
	}
}

// TestLoad_RejectsOutOfRange verifies numeric bounds.
func TestLoad_RejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		// batch_size=0 fills default (intentional, like LoadDefaults)
		{"batch too big", `{"endpoint":"https://x","batch_size":2000}`, "batch_size"},
		{"flush too big", `{"endpoint":"https://x","flush_ms":99999}`, "flush_ms"},
		{"retries too big", `{"endpoint":"https://x","retries":99}`, "retries"},
		{"empty endpoint", `{"endpoint":""}`, "endpoint is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestLoad_BatchZero_FillsDefault verifies BatchSize=0 is treated as "use default".
func TestLoad_BatchZero_FillsDefault(t *testing.T) {
	c, err := Load(`{"endpoint":"https://x","batch_size":0}`)
	if err != nil {
		t.Fatalf("expected 0 to fill default, got error: %v", err)
	}
	if c.BatchSize != 100 {
		t.Errorf("BatchSize = %d, want 100 (default)", c.BatchSize)
	}
}

// TestLoad_RejectsUnknownFormat catches typos like "cef2".
func TestLoad_RejectsUnknownFormat(t *testing.T) {
	_, err := Load(`{"endpoint":"https://x","format":"cef2"}`)
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
	if !strings.Contains(err.Error(), "format") {
		t.Errorf("error should mention format, got: %v", err)
	}
}

// TestLoad_RejectsEmptyExtraFieldKey catches malformed extra fields.
func TestLoad_RejectsEmptyExtraFieldKey(t *testing.T) {
	_, err := Load(`{"endpoint":"https://x","extra_fields":{"":"v"}}`)
	if err == nil {
		t.Fatal("expected error for empty key in extra_fields")
	}
}

// TestIsRemote verifies the endpoint classifier.
// Only https:// is considered remote — http:// is rejected at Validate()
// (plaintext CEF leak risk) so it never reaches the worker.
func TestIsRemote(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		want     bool
	}{
		{"/var/log/siem.log", false},
		{"./local.log", false},
		{"https://splunk.example.com/x", true},
		// http:// rejected by Validate, but IsRemote returns false (it's
		// not a real remote destination; it's a config error).
		{"http://x.example.com", false},
	} {
		c := Config{Endpoint: tc.endpoint}
		if got := c.IsRemote(); got != tc.want {
			t.Errorf("IsRemote(%q) = %v, want %v", tc.endpoint, got, tc.want)
		}
	}
}

// TestValidFormats_Stable guards the supported format vocabulary.
func TestValidFormats_Stable(t *testing.T) {
	for _, f := range []Format{WireCEF, WireLEEF, WireJSON} {
		if !ValidFormats[f] {
			t.Errorf("format %q missing from ValidFormats", f)
		}
	}
}

// TestSettingsKey_Documented verifies the canonical settings_kv key.
func TestSettingsKey_Documented(t *testing.T) {
	if SettingsKey != "siem.config" {
		t.Errorf("SettingsKey = %q, want siem.config (operator configs depend on this)", SettingsKey)
	}
}
