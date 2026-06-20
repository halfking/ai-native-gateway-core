package settings

import (
	"testing"
)

func TestSpec_Validate_Int(t *testing.T) {
	min := 1.0
	max := 100.0
	s := &Spec{Type: TypeInt, Min: &min, Max: &max}

	cases := []struct {
		name    string
		in      any
		wantErr bool
	}{
		{"valid int", 50, false},
		{"valid int64", int64(50), false},
		{"valid float as int", float64(50.0), false},
		{"below min", 0, true},
		{"above max", 101, true},
		{"wrong type", "50", true},
		{"nil", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Validate(tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("got err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestSpec_Validate_Float(t *testing.T) {
	min := 0.0
	max := 1.0
	s := &Spec{Type: TypeFloat, Min: &min, Max: &max}
	if err := s.Validate(0.5); err != nil {
		t.Errorf("valid float failed: %v", err)
	}
	if err := s.Validate(1.5); err == nil {
		t.Errorf("out of range should fail")
	}
	if err := s.Validate("x"); err == nil {
		t.Errorf("wrong type should fail")
	}
}

func TestSpec_Validate_Bool(t *testing.T) {
	s := &Spec{Type: TypeBool}
	if err := s.Validate(true); err != nil {
		t.Errorf("valid bool failed: %v", err)
	}
	if err := s.Validate("true"); err == nil {
		t.Errorf("string should fail")
	}
}

func TestSpec_Validate_Enum(t *testing.T) {
	s := &Spec{Type: TypeEnum, Options: []string{"a", "b", "c"}}
	if err := s.Validate("a"); err != nil {
		t.Errorf("valid enum failed: %v", err)
	}
	if err := s.Validate("d"); err == nil {
		t.Errorf("invalid enum should fail")
	}
	if err := s.Validate(123); err == nil {
		t.Errorf("wrong type should fail")
	}
}

func TestSpec_Validate_String(t *testing.T) {
	s := &Spec{Type: TypeString}
	if err := s.Validate("hello"); err != nil {
		t.Errorf("valid string failed: %v", err)
	}
	if err := s.Validate(123); err == nil {
		t.Errorf("wrong type should fail")
	}
}

func TestEnvNameAuto(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"compression.mode", "LLM_GATEWAY_COMPRESSION_MODE"},
		{"rate_limit_rpm", "LLM_GATEWAY_RATE_LIMIT_RPM"},
		{"simple", "LLM_GATEWAY_SIMPLE"},
	}
	for _, tc := range cases {
		if got := EnvNameAuto(tc.in); got != tc.want {
			t.Errorf("EnvNameAuto(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRegistry_RegisterSpec(t *testing.T) {
	r := NewRegistry()
	s1 := &Spec{Key: "a.b", Type: TypeString, Default: "x"}
	s2 := &Spec{Key: "a.b", Type: TypeString, Default: "y"} // duplicate
	if err := r.RegisterSpec(s1); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.RegisterSpec(s2); err == nil {
		t.Error("duplicate should fail")
	}
	if r.Spec("a.b") == nil {
		t.Error("lookup failed")
	}
	if r.Spec("missing") != nil {
		t.Error("expected nil for missing key")
	}
}

func TestRegistry_EffectiveValue_FallbackChain(t *testing.T) {
	r := NewRegistry()
	// No DB backend, no env backend → defaults.
	spec := &Spec{Key: "test", Type: TypeString, Default: "the-default"}
	r.MustRegisterSpec(spec)

	v, src, err := r.EffectiveValue(ScopePlatform, "test", "")
	if err != nil {
		t.Fatalf("EffectiveValue: %v", err)
	}
	if src != "default" {
		t.Errorf("src = %q, want default", src)
	}
	if string(v) != `"the-default"` {
		t.Errorf("v = %s, want \"the-default\"", v)
	}
}

func TestRegistry_EffectiveValue_Env(t *testing.T) {
	r := NewRegistry()
	r.RegisterBackend(EnvBackendScope, NewStoreEnv())
	spec := &Spec{Key: "test", Type: TypeString, Default: "the-default"}
	r.MustRegisterSpec(spec)

	t.Setenv("LLM_GATEWAY_TEST", "\"from-env\"")
	v, src, err := r.EffectiveValue(ScopePlatform, "test", "")
	if err != nil {
		t.Fatalf("EffectiveValue: %v", err)
	}
	if src != "env" {
		t.Errorf("src = %q, want env", src)
	}
	if string(v) != `"from-env"` {
		t.Errorf("v = %s, want from-env", v)
	}
}

func TestRegistry_EffectiveValue_DB_Beats_Env(t *testing.T) {
	r := NewRegistry()
	r.RegisterBackend(ScopePlatform, &fakeBackend{
		store: map[string][]byte{
			"test": []byte(`"from-db"`),
		},
	})
	r.RegisterBackend(EnvBackendScope, NewStoreEnv())
	spec := &Spec{Key: "test", Type: TypeString, Default: "the-default"}
	r.MustRegisterSpec(spec)

	t.Setenv("LLM_GATEWAY_TEST", "\"from-env\"")
	v, src, err := r.EffectiveValue(ScopePlatform, "test", "")
	if err != nil {
		t.Fatalf("EffectiveValue: %v", err)
	}
	if src != "db" {
		t.Errorf("src = %q, want db (DB should beat env)", src)
	}
	if string(v) != `"from-db"` {
		t.Errorf("v = %s, want from-db", v)
	}
}

func TestRegistry_EffectiveValue_UnknownKey(t *testing.T) {
	r := NewRegistry()
	_, _, err := r.EffectiveValue(ScopePlatform, "missing", "")
	if err == nil {
		t.Error("expected error for unknown key")
	}
}

// fakeBackend is a minimal in-memory Backend for testing.
type fakeBackend struct {
	store map[string][]byte
}

func (f *fakeBackend) Get(_ Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeBackend) Set(_ Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}
func (f *fakeBackend) GetTenant(_, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeBackend) SetTenant(_, _ string, _ any) ([]byte, error) {
	return nil, nil
}
