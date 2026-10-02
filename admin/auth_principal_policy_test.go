package admin

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/kaixuan/llm-gateway-go/pkg/identity"
)

// This fixture exercises the production HTTP auth middleware and principal
// context. It intentionally has no database or session-resource handler.
func requestPrincipalPolicy(t *testing.T, raw, transport string) (int, int32, *AuthContext) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(AdminMiddleware(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(GetAuthContext(r)); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}, nil, legacySecret))
	defer srv.Close()
	path := srv.URL + "/api/admin/principal-policy-fixture"
	if transport == "query" {
		path += "?token=" + raw
	}
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal("fixture request failed")
	}
	switch transport {
	case "cookie":
		req.AddCookie(&http.Cookie{Name: CookieName, Value: raw})
	case "query":
	default:
		req.Header.Set("Authorization", "Bearer "+raw)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal("fixture HTTP transport failed")
	}
	defer resp.Body.Close()
	var principal *AuthContext
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&principal); err != nil {
			t.Fatal("fixture principal response failed")
		}
	}
	return resp.StatusCode, calls.Load(), principal
}

func signPolicyLegacy(t *testing.T, tenant, role string) string {
	t.Helper()
	raw, _, err := SignToken(42, tenant, "alice", role, legacySecret, false)
	if err != nil {
		t.Fatal("legacy fixture signing failed")
	}
	return raw
}

func TestAdminPrincipalPolicyRejectsLegacyRetry(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", legacySecret)
	t.Setenv(identity.EnvSharedSecret, "")
	identity.ResetForTest()
	t.Cleanup(identity.ResetForTest)
	for _, tc := range []struct{ name, tenant, role string }{
		{"missing role", "tenant-a", ""},
		{"unknown role", "tenant-a", "external_role"},
		{"legacy key marker", "tenant-a", "admin_key"},
		{"padded role", "tenant-a", " user "},
		{"missing tenant", "", "user"},
		{"blank tenant", " ", "user"},
		{"padded tenant", " tenant-a ", "user"},
	} {
		for _, transport := range []string{"bearer", "cookie", "query"} {
			t.Run(tc.name+" "+transport, func(t *testing.T) {
				status, calls, _ := requestPrincipalPolicy(t, signPolicyLegacy(t, tc.tenant, tc.role), transport)
				if status != http.StatusUnauthorized || calls != 0 {
					t.Fatalf("invalid principal must not be rescued by an unnormalized retry: status=%d calls=%d", status, calls)
				}
			})
		}
	}
}

func TestAdminPrincipalPolicySharedBoundary(t *testing.T) {
	withSharedSecret(t)
	t.Setenv("LLM_GATEWAY_JWT_SECRET", legacySecret)
	t.Setenv(identity.EnvExpectedAudience, identity.DefaultAudience)
	wrapToOne := new(big.Int).Lsh(big.NewInt(1), uint(strconv.IntSize))
	wrapToOne.Add(wrapToOne, big.NewInt(1))
	for _, tc := range []struct {
		name       string
		roles      []string
		scope      string
		tenant, id string
	}{
		{"missing role", nil, "", "tenant-a", "42"},
		{"scope-only role", nil, "tenant_admin", "tenant-a", "42"},
		{"unknown primary role", []string{"external_role", "super_admin"}, "", "tenant-a", "42"},
		{"missing tenant", []string{"user"}, "", "", "42"},
		{"overflow user", []string{"user"}, "", "tenant-a", wrapToOne.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := mintMultiIssuerForTest(t, "acc", "alice", identity.DefaultAudience, sharedSecret, time.Hour, jwt.MapClaims{
				"user_id": tc.id, "tenant_id": tc.tenant, "roles": tc.roles, "scope": tc.scope,
			})
			status, calls, _ := requestPrincipalPolicy(t, raw, "bearer")
			if status != http.StatusUnauthorized || calls != 0 {
				t.Fatalf("shared principal must reject before handlers: status=%d calls=%d", status, calls)
			}
		})
	}
}

func TestAdminPrincipalPolicyPreservesExplicitTiers(t *testing.T) {
	withSharedSecret(t)
	t.Setenv("LLM_GATEWAY_JWT_SECRET", legacySecret)
	t.Setenv(identity.EnvExpectedAudience, identity.DefaultAudience)
	for _, role := range []string{"user", "tenant_admin", "super_admin"} {
		for _, transport := range []string{"bearer", "cookie", "query"} {
			for _, source := range []string{"shared", "legacy"} {
				t.Run(source+" "+role+" "+transport, func(t *testing.T) {
					raw := signPolicyLegacy(t, "tenant-a", role)
					if source == "shared" {
						raw = mintMultiIssuerForTest(t, "acc", "alice", identity.DefaultAudience, sharedSecret, time.Hour, jwt.MapClaims{
							"user_id": "42", "tenant_id": "tenant-a", "roles": []string{role}, "scope": "super_admin",
						})
					}
					status, calls, p := requestPrincipalPolicy(t, raw, transport)
					if status != http.StatusOK || calls != 1 || p == nil || p.Role != role || p.UserID != 42 || p.Username != "alice" || p.TenantID != "tenant-a" || !p.IsJWT {
						t.Fatal("explicit original principal and tier must reach the handler unchanged")
					}
				})
			}
		}
	}
}

func TestAdminPrincipalPolicyCanonicalLegacyWithEqualSecrets(t *testing.T) {
	withSharedSecret(t)
	t.Setenv("LLM_GATEWAY_JWT_SECRET", sharedSecret)
	status, calls, p := requestPrincipalPolicy(t, signPolicyLegacy(t, "tenant-a", "user"), "bearer")
	if status != http.StatusOK || calls != 1 || p == nil || p.Role != "user" {
		t.Fatal("canonical signed legacy user token must still work when the configured secrets coincide")
	}
}
