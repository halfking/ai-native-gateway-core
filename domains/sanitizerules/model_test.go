package sanitizerules

import (
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{Rules: []Rule{{ID: "secret", Name: "Secret", Type: "secret", Kind: "pattern", Pattern: `password=(\w+)`, Group: 1, Scopes: []string{"input"}, Action: "mask", Enabled: true, Samples: []Sample{{Text: "password=alpha", Match: true}, {Text: "ordinary text", Match: false}}}}}
}

func TestCompileValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty", func(c *Config) { c.Rules = nil }},
		{"regexp", func(c *Config) { c.Rules[0].Pattern = "(" }},
		{"group", func(c *Config) { c.Rules[0].Group = 2 }},
		{"duplicate", func(c *Config) { c.Rules = append(c.Rules, c.Rules[0]) }},
		{"phase", func(c *Config) { c.Rules[0].Scopes = []string{"sideways"} }},
		{"action", func(c *Config) { c.Rules[0].Action = "execute" }},
		{"kind", func(c *Config) { c.Rules[0].Kind = "script" }},
		{"sample", func(c *Config) { c.Rules[0].Samples[0].Match = false }},
		{"semantic scope", func(c *Config) { c.Rules[0].Kind = "credential_declaration"; c.Rules[0].Scopes = []string{"output"} }},
		{"egress mask", func(c *Config) { c.Rules[0].Scopes = []string{"egress"} }},
		{"priority", func(c *Config) { c.Rules[0].Priority = 10001 }},
		{"identifier", func(c *Config) { c.Rules[0].ID = "password=raw-secret" }},
		{"oversized pattern", func(c *Config) { c.Rules[0].Pattern = strings.Repeat("a", 16385) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConfig()
			tt.mutate(&c)
			if _, err := Compile(c, 5); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestCompileDetachesAndOrders(t *testing.T) {
	c := testConfig()
	second := c.Rules[0]
	second.ID = "first"
	second.Priority = 50
	c.Rules = append(c.Rules, second)
	snap, err := Compile(c, 7)
	if err != nil {
		t.Fatal(err)
	}
	c.Rules[0].Scopes[0] = "egress"
	c.Rules[0].Samples[0].Text = "changed"
	if snap.Version != 7 || snap.Rules[0].ID != "first" {
		t.Fatal("snapshot version/order incorrect")
	}
	if !snap.Rules[1].Applies("input", "tenant", "", "field") || !snap.Rules[1].MatchString("password=alpha") {
		t.Fatal("compiled rule lost immutable configuration")
	}
	copy := snap.Config()
	copy.Rules[0].Scopes[0] = "output"
	if snap.Rules[0].Scopes[0] != "input" {
		t.Fatal("Config aliases snapshot")
	}
}

func TestScopeTenantToolFieldAndInputBlock(t *testing.T) {
	c := testConfig()
	r := &c.Rules[0]
	r.Action = "block"
	r.TenantID = "tenant-a"
	r.Tools = []string{"shell"}
	r.Fields = []string{"command"}
	snap, err := Compile(c, 1)
	if err != nil {
		t.Fatal(err)
	}
	rule := snap.Rules[0]
	if !rule.Applies("input", "tenant-a", "shell", "command") {
		t.Fatal("matching scope rejected")
	}
	for _, args := range [][4]string{{"output", "tenant-a", "shell", "command"}, {"input", "tenant-b", "shell", "command"}, {"input", "tenant-a", "other", "command"}, {"input", "tenant-a", "shell", "other"}} {
		if rule.Applies(args[0], args[1], args[2], args[3]) {
			t.Fatal("scope escaped")
		}
	}
}

func TestValidationErrorsDoNotEchoSamplesOrPatterns(t *testing.T) {
	c := testConfig()
	c.Rules[0].Pattern = "(super-secret"
	c.Rules[0].Samples[0].Text = "super-secret"
	_, err := Compile(c, 1)
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("unsafe validation error: %v", err)
	}
}
func TestVendorKeyShapeIsAnExplicitKind(t *testing.T) {
	c := testConfig()
	c.Rules[0].Kind = "vendor_key_shape"
	c.Rules[0].Scopes = []string{"input", "output", "egress"}
	c.Rules[0].Action = "block"
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
}
