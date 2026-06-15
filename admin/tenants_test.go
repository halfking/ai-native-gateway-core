package admin

import "testing"

func TestDefaultTenantAdminUsername(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"acme", "acmeuser"},
		{"testco", "testcouser"},
		{"my_tenant", "my_tenantuser"},
	}
	for _, tt := range tests {
		if got := DefaultTenantAdminUsername(tt.code); got != tt.want {
			t.Errorf("DefaultTenantAdminUsername(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestGenerateTenantAdminPassword(t *testing.T) {
	pw := GenerateTenantAdminPassword("testco")
	if pw != "Tenanttestco@2026" {
		t.Fatalf("unexpected password format: %q", pw)
	}
	if err := ValidatePasswordComplexity(pw); err != nil {
		t.Fatalf("password should pass complexity: %v", err)
	}
}

func TestIsValidTenantCode(t *testing.T) {
	valid := []string{"acme", "test_co", "a1", "ab"}
	for _, c := range valid {
		if !isValidTenantCode(c) {
			t.Errorf("expected valid: %q", c)
		}
	}
	invalid := []string{"", "A", "ACME", "a", "-bad"}
	for _, c := range invalid {
		if isValidTenantCode(c) {
			t.Errorf("expected invalid: %q", c)
		}
	}
}
