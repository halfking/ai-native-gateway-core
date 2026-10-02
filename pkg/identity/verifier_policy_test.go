package identity

import (
	"errors"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/identity/token"
)

type policyLegacyVerifier struct {
	claims *LegacyClaims
	calls  int
}

func (v *policyLegacyVerifier) VerifyLegacy(string) (*LegacyClaims, error) {
	v.calls++
	return v.claims, nil
}

func policyToken(t *testing.T, roles []string, scope, tenant, userID string) string {
	t.Helper()
	raw, err := token.SignHS256(token.Issuer{Name: "acc", Secret: []byte(testSharedSecret)}, &token.Claims{
		Subject: "alice", UserID: userID, TenantID: tenant,
		Audience: DefaultAudience, Roles: roles, Scope: scope,
	}, time.Hour)
	if err != nil {
		t.Fatal("signing fixture failed")
	}
	return raw
}

func TestVerifierPrincipalRolePolicy(t *testing.T) {
	withSharedSecret(t)
	for _, tc := range []struct {
		name  string
		roles []string
		scope string
	}{
		{"absent", nil, ""},
		{"empty", []string{}, ""},
		{"blank", []string{"", "  "}, ""},
		{"empty primary before administrator", []string{"", "super_admin"}, ""},
		{"blank primary before administrator", []string{"  ", "tenant_admin"}, ""},
		{"scope is not a role", nil, "tenant_admin session:read"},
		{"unknown", []string{"external_role"}, ""},
		{"unknown before administrator", []string{"external_role", "tenant_admin"}, ""},
		{"padded", []string{" tenant_admin "}, ""},
		{"legacy key pseudorole", []string{"admin_key"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := &policyLegacyVerifier{claims: &LegacyClaims{UserID: 9, TenantID: "tenant-a", Role: "super_admin"}}
			p, err := Verify(policyToken(t, tc.roles, tc.scope, "tenant-a", "42"), fallback)
			if !errors.Is(err, ErrInvalidToken) || p != nil {
				t.Fatal("invalid explicit role must reject the normalized principal")
			}
			if fallback.calls != 0 {
				t.Fatal("verified shared-principal policy rejection must not use legacy fallback")
			}
		})
	}
	for _, role := range []string{"user", "tenant_admin", "super_admin"} {
		t.Run("explicit "+role, func(t *testing.T) {
			p, err := Verify(policyToken(t, []string{role}, "tenant_admin unrelated:scope", "tenant-a", "42"), nil)
			if err != nil || p == nil || p.Role != role || p.UserID != 42 || p.Username != "alice" || p.TenantID != "tenant-a" || p.Issuer != "acc" || p.Audience != DefaultAudience {
				t.Fatal("explicit supported role and original principal must be preserved")
			}
		})
	}
}

func TestVerifierLegacyPrincipalRolePolicy(t *testing.T) {
	t.Setenv(EnvSharedSecret, "")
	ResetForTest()
	t.Cleanup(ResetForTest)
	for _, role := range []string{"", "external_role", "admin_key", " user "} {
		t.Run("reject "+role, func(t *testing.T) {
			p, err := Verify("legacy-fixture", &policyLegacyVerifier{claims: &LegacyClaims{UserID: 42, TenantID: "tenant-a", Role: role}})
			if !errors.Is(err, ErrInvalidToken) || p != nil {
				t.Fatal("legacy principal must also have an explicit supported role")
			}
		})
	}
	issuedAt := time.Now().Add(-time.Minute)
	for _, role := range []string{"user", "tenant_admin", "super_admin"} {
		p, err := Verify("legacy-fixture", &policyLegacyVerifier{claims: &LegacyClaims{
			UserID: 42, TenantID: "tenant-a", Username: "alice", Role: role,
			IssuedAt: issuedAt, MustChangePassword: true,
		}})
		if err != nil || p == nil || p.Role != role || p.UserID != 42 || p.Username != "alice" || !p.MustChangePassword || !p.IssuedAt.Equal(issuedAt) || p.Source != "legacy" {
			t.Fatal("legitimate legacy role and password/revocation metadata must be preserved")
		}
	}
}

func TestVerifierPrincipalTenantPolicy(t *testing.T) {
	withSharedSecret(t)
	for _, tenant := range []string{"", " ", " tenant-a", "tenant-a "} {
		t.Run("shared "+tenant, func(t *testing.T) {
			fallback := &policyLegacyVerifier{claims: &LegacyClaims{UserID: 9, TenantID: "tenant-a", Role: "super_admin"}}
			p, err := Verify(policyToken(t, []string{"tenant_admin"}, "", tenant, "42"), fallback)
			if !errors.Is(err, ErrInvalidToken) || p != nil || fallback.calls != 0 {
				t.Fatal("shared principal must reject absent or noncanonical tenant without fallback")
			}
		})
		t.Run("legacy "+tenant, func(t *testing.T) {
			p, err := Verify("legacy-fixture", &policyLegacyVerifier{claims: &LegacyClaims{UserID: 42, TenantID: tenant, Role: "user"}})
			if !errors.Is(err, ErrInvalidToken) || p != nil {
				t.Fatal("legacy principal must reject absent or noncanonical tenant")
			}
		})
	}
	for _, tenant := range []string{"tenant-a", "default"} {
		p, err := Verify(policyToken(t, []string{"user"}, "", tenant, "42"), nil)
		if err != nil || p == nil || p.TenantID != tenant {
			t.Fatal("explicit canonical tenant must be preserved")
		}
	}
}

func TestVerifierPrincipalNumericBridge(t *testing.T) {
	withSharedSecret(t)
	wrapToOne := new(big.Int).Lsh(big.NewInt(1), uint(strconv.IntSize))
	wrapToOne.Add(wrapToOne, big.NewInt(1))
	for _, tc := range []struct {
		name, input string
		want        int
	}{
		{"positive", "42", 42},
		{"leading zeros", "00042", 42},
		{"max int", strconv.Itoa(int(^uint(0) >> 1)), int(^uint(0) >> 1)},
		{"overflow alias", wrapToOne.String(), 0},
		{"very long overflow", wrapToOne.String() + "00000000000000000000", 0},
		{"foreign subject", "acc-user-42", 0},
		{"absent", "", 0},
		{"zero", "0", 0},
		{"negative", "-42", 0},
		{"plus", "+42", 0},
		{"non ASCII digits", "٤٢", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Verify(policyToken(t, []string{"user"}, "", "tenant-a", tc.input), nil)
			if err != nil || p == nil || p.UserID != tc.want {
				t.Fatal("numeric bridge must preserve valid IDs and never create a wrapped local identity")
			}
		})
	}
}
