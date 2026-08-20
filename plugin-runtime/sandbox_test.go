package pluginruntime

import (
	"testing"
)

func sandboxBinding(id string, caps []string) PluginBinding {
	return PluginBinding{
		PluginID:         "plugin-x",
		BindingID:        id,
		Phase:            PhaseRequest,
		Priority:         1,
		ExecutionMode:    ExecutionSequential,
		Capabilities:     caps,
		TimeoutMillis:    1000,
		ConcurrencyLimit: 1,
		FailurePolicy:    FailureClosed,
		DTOProfile:       DTOProfileRedacted,
		Enabled:          true,
	}
}

func TestSandboxPolicy_DefaultDenyByDefault(t *testing.T) {
	p := DefaultSandboxPolicy()
	if p.Available {
		t.Fatalf("default sandbox must be unavailable (zero-trust)")
	}
	if p.MaxConcurrent != 1 {
		t.Fatalf("default max_concurrent = %d, want 1", p.MaxConcurrent)
	}
	if err := ValidateSandboxPolicy(p); err != nil {
		t.Fatalf("default policy should validate: %v", err)
	}
}

func TestSandboxPolicy_ValidateRequiresRealSandbox(t *testing.T) {
	tests := []struct {
		name    string
		policy  SandboxPolicy
		wantErr bool
	}{
		{"available without os user", SandboxPolicy{Available: true, MemoryBytes: 1 << 30, MaxConcurrent: 1}, true},
		{"available without memory", SandboxPolicy{Available: true, OSUser: "plugin", MaxConcurrent: 1}, true},
		{"available valid", SandboxPolicy{Available: true, OSUser: "plugin", MemoryBytes: 1 << 30, MaxConcurrent: 2}, false},
		{"unavailable always valid", SandboxPolicy{Available: false}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSandboxPolicy(tt.policy)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSandboxPolicy() err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestSensitiveEnvKey(t *testing.T) {
	good := []string{"PATH", "TZ", "LANG", "PLUGIN_MODE"}
	bad := []string{"DB_PASSWORD", "API_KEY", "DATABASE_URL", "REDIS_DSN", "JWT_SECRET", "PRIVATE_KEY"}
	for _, k := range good {
		if sensitiveEnvKey(k) {
			t.Errorf("key %q should NOT be sensitive", k)
		}
	}
	for _, k := range bad {
		if !sensitiveEnvKey(k) {
			t.Errorf("key %q should be sensitive", k)
		}
	}
}

func TestSandboxEnforcer_AllowExecutionWithoutSandbox(t *testing.T) {
	e := NewSandboxEnforcer(nil) // zero-trust default
	if e.Policy().Available {
		t.Fatalf("nil policy must imply no sandbox")
	}

	// observe-only binding is allowed even without sandbox
	observe := sandboxBinding("b1", []string{CapabilityRequestObserve})
	if err := e.AllowExecution(observe); err != nil {
		t.Fatalf("observe binding should be allowed without sandbox: %v", err)
	}

	// write capability must be refused without sandbox
	write := sandboxBinding("b2", []string{CapabilityRequestMutate})
	if err := e.AllowExecution(write); err == nil {
		t.Fatalf("write binding MUST be refused without sandbox")
	} else if !contains(err.Error(), "requires sandbox") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSandboxEnforcer_AllowExecutionWithSandbox(t *testing.T) {
	policy := SandboxPolicy{
		Available:     true,
		OSUser:        "plugin",
		MemoryBytes:   1 << 30,
		MaxConcurrent: 4,
		EnvAllowlist:  []string{"PATH", "PLUGIN_MODE"},
	}
	e := NewSandboxEnforcer(&policy)

	write := sandboxBinding("b1", []string{CapabilityRequestMutate, CapabilityRequestObserve})
	if err := e.AllowExecution(write); err != nil {
		t.Fatalf("write binding should be allowed with sandbox: %v", err)
	}
}

func TestSandboxEnforcer_AllowDataPlaneWrite(t *testing.T) {
	e := NewSandboxEnforcer(nil)
	write := sandboxBinding("b1", []string{CapabilityRequestMutate})
	if err := e.AllowDataPlaneWrite(write); err == nil {
		t.Fatalf("data-plane write must be refused without sandbox")
	}

	policy := SandboxPolicy{Available: true, OSUser: "plugin", MemoryBytes: 1 << 30, MaxConcurrent: 1}
	se := NewSandboxEnforcer(&policy)
	if err := se.AllowDataPlaneWrite(write); err != nil {
		t.Fatalf("data-plane write should pass with sandbox: %v", err)
	}

	observe := sandboxBinding("b2", []string{CapabilityRequestObserve})
	if err := se.AllowDataPlaneWrite(observe); err == nil {
		t.Fatalf("non-write binding must not satisfy AllowDataPlaneWrite")
	}
}

func TestSandboxEnforcer_BuildControlledEnv(t *testing.T) {
	base := map[string]string{
		"PATH":            "/usr/bin",
		"PLUGIN_MODE":     "strict",
		"DB_PASSWORD":     "supersecret",
		"DATABASE_URL":    "postgres://x",
		"LLM_GATEWAY_KEY": "tok-123",
	}
	policy := SandboxPolicy{
		Available:    true,
		OSUser:       "plugin",
		MemoryBytes:  1 << 30,
		EnvAllowlist: []string{"PATH", "PLUGIN_MODE", "DB_PASSWORD", "LLM_GATEWAY_KEY"},
	}
	e := NewSandboxEnforcer(&policy)

	out := e.BuildControlledEnv(base)
	if out["PATH"] != "/usr/bin" || out["PLUGIN_MODE"] != "strict" {
		t.Fatalf("allowlisted non-sensitive keys must be copied: %v", out)
	}
	if v, ok := out["DB_PASSWORD"]; ok {
		t.Fatalf("credential must be stripped even if allowlisted, got %q", v)
	}
	if _, ok := out["DATABASE_URL"]; ok {
		t.Fatalf("DSN must be stripped: %v", out)
	}
	if _, ok := out["LLM_GATEWAY_KEY"]; ok {
		t.Fatalf("API key must be stripped: %v", out)
	}
	if len(out) != 2 {
		t.Fatalf("expected exactly 2 safe keys, got %d: %v", len(out), out)
	}
}

func TestSandboxEnforcer_DTOExposureDowngrade(t *testing.T) {
	// Without sandbox, any stronger profile is forced to redacted.
	e := NewSandboxEnforcer(nil)

	none := sandboxBinding("b1", []string{CapabilityRequestObserve})
	none.DTOProfile = DTOProfileNone
	if p, err := e.DTOExposure(none); err != nil || p != DTOProfileNone {
		t.Fatalf("none profile must pass untouched: p=%v err=%v", p, err)
	}

	redacted := sandboxBinding("b2", []string{CapabilityRequestObserve})
	redacted.DTOProfile = DTOProfileRedacted
	if p, err := e.DTOExposure(redacted); err != nil || p != DTOProfileRedacted {
		t.Fatalf("redacted must stay redacted: p=%v err=%v", p, err)
	}

	// A binding requesting full data must be downgraded when no sandbox.
	full := sandboxBinding("b3", []string{CapabilityRequestObserve})
	full.DTOProfile = DTOProfileSummary
	if p, err := e.DTOExposure(full); err != nil || p != DTOProfileRedacted {
		t.Fatalf("no-sandbox must force redacted, got p=%v err=%v", p, err)
	}

	// With sandbox, declared profile is honored.
	policy := SandboxPolicy{Available: true, OSUser: "plugin", MemoryBytes: 1 << 30, MaxConcurrent: 1}
	se := NewSandboxEnforcer(&policy)
	if p, err := se.DTOExposure(full); err != nil || p != DTOProfileSummary {
		t.Fatalf("with sandbox declared profile honored, got p=%v err=%v", p, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
