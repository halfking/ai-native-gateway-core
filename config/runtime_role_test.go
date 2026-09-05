package config

import "testing"

func TestNormalizeRuntimeRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
		err  bool
	}{
		{name: "empty defaults active", want: RuntimeRoleActive},
		{name: "active", in: RuntimeRoleActive, want: RuntimeRoleActive},
		{name: "traffic only", in: RuntimeRoleTrafficOnly, want: RuntimeRoleTrafficOnly},
		{name: "unknown fails closed", in: "candidate", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRuntimeRole(tc.in)
			if tc.err {
				if err == nil {
					t.Fatal("NormalizeRuntimeRole() error = nil")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeRuntimeRole(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestValidateRuntimeRole(t *testing.T) {
	cfg := &Config{}
	if err := cfg.ValidateRuntimeRole(); err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeRole != RuntimeRoleActive {
		t.Fatalf("default role = %q, want active", cfg.RuntimeRole)
	}
	cfg.RuntimeRole = RuntimeRoleTrafficOnly
	if err := cfg.ValidateRuntimeRole(); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsTrafficOnly() {
		t.Fatal("traffic-only role not detected")
	}
}
