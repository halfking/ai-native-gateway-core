package pluginruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseValidV2 returns a manifest that passes every validator. Individual fields
// are overridden per-table-row to assert the targeted failure mode.
func baseValidV2() Manifest {
	return Manifest{
		SchemaVersion: 2,
		PluginID:      "ai-session-manager",
		DisplayName:   "AI Session Manager",
		PluginVersion: "1.0.0",
		BuildSeq:      1,
		Channel:       "dev",
		GatewayCompatibility: GatewayCompatibility{
			MinVersion:  "1.0.0",
			MaxVersion:  "2.0.0",
			APIContract: SupportedAPIContractV2,
		},
		Runtime: Runtime{
			Entrypoint:        "bin/ai-session-manager",
			Protocol:          "http-unix-socket",
			HealthPath:        "/plugin/healthz",
			HandshakePath:     "/plugin/handshake",
			ShutdownGraceSecs: 20,
		},
		Capabilities: []string{"session.read", "events.ingest"},
		Permissions:  []string{"session.read"},
		Hooks:        []string{"on_install", "on_activate"},
		Activation: Activation{
			ModuleKey: "session_manager",
			License:   License{Mode: "open"},
		},
		ConfigSchema: ConfigSchema{},
		Bindings:     []PluginBinding{},
	}
}

// writeManifest writes m to a temp file and calls validate. Returns the error
// from validate (nil == pass). Returning from LoadManifest so JSON re-marshal
// is not on the test path.
func writeManifest(t *testing.T, m Manifest) error {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = LoadManifest(p)
	return err
}

func TestManifestValidate_V1AndV2(t *testing.T) {
	// v1 with v1 contract — should pass
	m1 := baseValidV2()
	m1.SchemaVersion = 1
	m1.GatewayCompatibility.APIContract = SupportedAPIContract
	m1.PluginVersion = "1.2.3" // not strictly required for v1
	if err := writeManifest(t, m1); err != nil {
		t.Fatalf("v1 valid manifest rejected: %v", err)
	}
	// v2 with v2 contract — should pass
	m2 := baseValidV2()
	if err := writeManifest(t, m2); err != nil {
		t.Fatalf("v2 valid manifest rejected: %v", err)
	}
}

func TestManifestValidate_SchemaVersionContractMismatch(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m *Manifest)
		wantSub string
	}{
		{
			name:    "v2-with-v1-contract",
			mutate:  func(m *Manifest) { m.GatewayCompatibility.APIContract = SupportedAPIContract },
			wantSub: "schema_version 2 requires",
		},
		{
			name:    "v1-with-v2-contract",
			mutate:  func(m *Manifest) { m.SchemaVersion = 1; m.GatewayCompatibility.APIContract = SupportedAPIContractV2 },
			wantSub: "requires schema_version 2",
		},
		{
			name:    "unknown-contract",
			mutate:  func(m *Manifest) { m.GatewayCompatibility.APIContract = "gateway-plugin-v9" },
			wantSub: "unsupported api_contract",
		},
		{
			name:    "schema-version-3",
			mutate:  func(m *Manifest) { m.SchemaVersion = 3 },
			wantSub: "unsupported schema_version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseValidV2()
			tc.mutate(&m)
			err := writeManifest(t, m)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestManifestValidate_PermissionsMustSubsetCapabilities(t *testing.T) {
	cases := []struct {
		name    string
		perms   []string
		wantSub string
	}{
		{name: "subset-ok", perms: []string{"session.read"}, wantSub: ""},
		{name: "subset-multi-ok", perms: []string{"session.read", "events.ingest"}, wantSub: ""},
		{name: "not-declared", perms: []string{"session.write"}, wantSub: `permissions "session.write" is not declared`},
		{name: "duplicate", perms: []string{"session.read", "session.read"}, wantSub: "duplicate"},
		{name: "empty-value", perms: []string{""}, wantSub: "must not contain empty values"},
		{name: "with-whitespace", perms: []string{"  "}, wantSub: "must not contain empty values"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseValidV2()
			m.Permissions = tc.perms
			err := writeManifest(t, m)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected nil err, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestManifestValidate_PluginIDRules(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantSub string
	}{
		{name: "ok-lower", id: "ai-session-manager", wantSub: ""},
		{name: "ok-dotted", id: "com.acme.plugin", wantSub: ""},
		{name: "ok-numeric-trail", id: "plugin2", wantSub: ""},
		{name: "empty", id: "", wantSub: "plugin_id required"},
		{name: "uppercase", id: "Plugin", wantSub: "invalid plugin_id"},
		{name: "leading-dash", id: "-plugin", wantSub: "invalid plugin_id"},
		{name: "space", id: "ai session", wantSub: "invalid plugin_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseValidV2()
			m.PluginID = tc.id
			err := writeManifest(t, m)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected nil err, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestManifestValidate_Hooks(t *testing.T) {
	cases := []struct {
		name    string
		hooks   []string
		wantSub string
	}{
		{name: "empty-ok", hooks: nil, wantSub: ""},
		{name: "all-valid", hooks: []string{"on_install", "on_activate", "on_deactivate", "on_uninstall"}, wantSub: ""},
		{name: "one-valid", hooks: []string{"on_install"}, wantSub: ""},
		{name: "unknown-hook", hooks: []string{"on_kaboom"}, wantSub: `unsupported hook "on_kaboom"`},
		{name: "duplicate", hooks: []string{"on_install", "on_install"}, wantSub: "duplicate hook"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseValidV2()
			m.Hooks = tc.hooks
			err := writeManifest(t, m)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestManifestValidate_License(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		wantSub string
	}{
		{name: "empty-defaults-open", mode: "", wantSub: ""},
		{name: "open", mode: "open", wantSub: ""},
		{name: "entitlement", mode: "entitlement", wantSub: ""},
		{name: "offline-cert", mode: "offline-cert", wantSub: ""},
		{name: "unknown", mode: "trial", wantSub: `unsupported license mode "trial"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseValidV2()
			m.Activation.License = License{Mode: tc.mode}
			err := writeManifest(t, m)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestManifestValidate_ConfigSchema(t *testing.T) {
	cases := []struct {
		name    string
		schema  ConfigSchema
		wantSub string
	}{
		{name: "nil", schema: nil, wantSub: ""},
		{name: "empty-map", schema: ConfigSchema{}, wantSub: ""},
		{
			name: "valid-nested",
			schema: ConfigSchema{
				"listen": map[string]any{"type": "string"},
				"limits": map[string]any{
					"qps":  map[string]any{"type": "number"},
					"size": map[string]any{"type": "integer"},
				},
			},
			wantSub: "",
		},
		{
			name:    "invalid-key-leading-digit",
			schema:  ConfigSchema{"1bad": "x"},
			wantSub: "invalid config_schema key",
		},
		{
			name:    "invalid-key-symbol",
			schema:  ConfigSchema{"bad key": "x"},
			wantSub: "invalid config_schema key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseValidV2()
			m.ConfigSchema = tc.schema
			err := writeManifest(t, m)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestManifestValidate_Runtime(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m *Manifest)
		wantSub string
	}{
		{
			name:    "abs-entrypoint",
			mutate:  func(m *Manifest) { m.Runtime.Entrypoint = "/etc/passwd" },
			wantSub: "must remain inside plugin directory",
		},
		{
			name:    "parent-escape",
			mutate:  func(m *Manifest) { m.Runtime.Entrypoint = "../bin/x" },
			wantSub: "must remain inside plugin directory",
		},
		{
			name:    "no-entrypoint",
			mutate:  func(m *Manifest) { m.Runtime.Entrypoint = "" },
			wantSub: "runtime entrypoint required",
		},
		{
			name:    "bad-protocol",
			mutate:  func(m *Manifest) { m.Runtime.Protocol = "tcp" },
			wantSub: "unsupported runtime protocol",
		},
		{
			name:    "no-handshake-path",
			mutate:  func(m *Manifest) { m.Runtime.HandshakePath = "" },
			wantSub: "absolute",
		},
		{
			name:    "no-health-path",
			mutate:  func(m *Manifest) { m.Runtime.HealthPath = "" },
			wantSub: "absolute",
		},
		{
			name:    "negative-grace",
			mutate:  func(m *Manifest) { m.Runtime.ShutdownGraceSecs = -1 },
			wantSub: "shutdown_grace_seconds",
		},
		{
			name:    "huge-grace",
			mutate:  func(m *Manifest) { m.Runtime.ShutdownGraceSecs = 999 },
			wantSub: "shutdown_grace_seconds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseValidV2()
			tc.mutate(&m)
			err := writeManifest(t, m)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidSemVer(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1.0.0", true},
		{"0.0.0", true},
		{"10.20.30", true},
		{"1.0.0-alpha", true},
		{"1.0.0-alpha.1", true},
		{"1.0.0-0.3.7", true},
		{"1.0.0-x-y-z.--", true},
		{"1.0.0+20130313144700", true},
		{"1.0.0-beta+exp.sha.5114f85", true},
		// invalid
		{"1", false},
		{"1.0", false},
		{"01.0.0", false},
		{"1.0.0-", false},
		{"v1.0.0", false},
		{"", false},
		{"a.b.c", false},
	}
	for _, tc := range cases {
		got := validSemVer(tc.in)
		if got != tc.want {
			t.Errorf("validSemVer(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCompareSemVer(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"2.0.0", "1.9.9", 1},
		{"0.9.0", "1.0.0", -1},
		{"1.0.0-alpha", "1.0.0", -1}, // prerelease < release
		{"1.0.0", "1.0.0-alpha", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1}, // more components wins over fewer
		{"1.0.0+build1", "1.0.0+build2", 0}, // build metadata ignored
	}
	for _, tc := range cases {
		got := CompareSemVer(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("CompareSemVer(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}