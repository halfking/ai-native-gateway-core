package pluginruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestEntitlementClientSignsTenantScopedServiceToken(t *testing.T) {
	const secret = "gateway-jwt-secret"
	startedAt := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if got, want := r.URL.EscapedPath(), "/maintain-api/internal/modules/session_manager/entitlement"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if r.URL.RawQuery != "" {
			t.Fatalf("query = %q, want empty", r.URL.RawQuery)
		}

		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			t.Fatalf("authorization = %q, want bearer token", authorization)
		}
		token, err := jwt.Parse(strings.TrimPrefix(authorization, "Bearer "), func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				t.Fatalf("signing method = %v, want HS256", token.Method)
			}
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			t.Fatalf("parse signed token: token=%v err=%v", token, err)
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			t.Fatalf("claims type = %T, want jwt.MapClaims", token.Claims)
		}
		if got, want := claims["tenant_id"], "tenant-a"; got != want {
			t.Fatalf("tenant_id = %v, want %q", got, want)
		}
		if got, want := claims["username"], "service:gateway"; got != want {
			t.Fatalf("username = %v, want %q", got, want)
		}
		if got, want := claims["role"], "service"; got != want {
			t.Fatalf("role = %v, want %q", got, want)
		}
		if got, want := claims["iss"], "ai-native-gateway"; got != want {
			t.Fatalf("issuer = %v, want %q", got, want)
		}
		scopes, ok := claims["scopes"].([]any)
		if !ok || len(scopes) != 1 || scopes[0] != "module:entitlement:read" {
			t.Fatalf("scopes = %#v, want module entitlement read scope", claims["scopes"])
		}
		expiresAt, err := claims.GetExpirationTime()
		if err != nil || expiresAt == nil || expiresAt.Time.Before(startedAt.Add(29*time.Second)) || expiresAt.Time.After(startedAt.Add(31*time.Second)) {
			t.Fatalf("expiry = %v, err=%v; want approximately 30 seconds after request", expiresAt, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"module_id":"session_manager","active":true,"effective_status":"opened"}`))
	}))
	defer server.Close()

	client := NewEntitlementClient(server.URL, secret, "ai-native-gateway")
	active, err := client.Allowed(context.Background(), "tenant-a", "session_manager")
	if err != nil || !active {
		t.Fatalf("Allowed() = %v, %v; want true, nil", active, err)
	}
}

func TestEntitlementClientReturnsFalseForInactive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"module_id":"session_manager","active":false,"effective_status":"inactive"}`))
	}))
	defer server.Close()

	client := NewEntitlementClient(server.URL, "secret", "ai-native-gateway")
	active, err := client.Allowed(context.Background(), "tenant-a", "session_manager")
	if err != nil || active {
		t.Fatalf("Allowed() = %v, %v; want false, nil", active, err)
	}
}

func TestEntitlementClientFailsClosedOnServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewEntitlementClient(server.URL, "secret", "ai-native-gateway")
	active, err := client.Allowed(context.Background(), "tenant-a", "session_manager")
	if err == nil || active {
		t.Fatalf("Allowed() = %v, %v; want false, non-nil error", active, err)
	}
}

func TestEntitlementClientFailsClosedOnMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true}`))
	}))
	defer server.Close()

	client := NewEntitlementClient(server.URL, "secret", "ai-native-gateway")
	active, err := client.Allowed(context.Background(), "tenant-a", "session_manager")
	if err == nil || active {
		t.Fatalf("Allowed() = %v, %v; want false, non-nil error", active, err)
	}
}

func TestEntitlementClientEscapesModulePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/maintain-api/internal/modules/module%2Fchild/entitlement"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"module_id":"module/child","active":true,"effective_status":"opened"}`))
	}))
	defer server.Close()

	client := NewEntitlementClient(server.URL, "secret", "ai-native-gateway")
	active, err := client.Allowed(context.Background(), "tenant-a", "module/child")
	if err != nil || !active {
		t.Fatalf("Allowed() = %v, %v; want true, nil", active, err)
	}
}
