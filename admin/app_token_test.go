package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/identity"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// The app ticket is the whole single sign-on handshake on the gateway side:
// whoever can call this endpoint gets a token the peer service will accept.
// These tests pin the three properties that make it safe to expose —
// audience scoping, short lifetime, and fail-closed behaviour when the
// identity mesh is not configured.

const testMeshSecret = "app-token-test-shared-secret-32-bytes!"

func withMeshSecret(t *testing.T) {
	t.Helper()
	t.Setenv(identity.EnvSharedSecret, testMeshSecret)
	identity.ResetForTest()
	t.Cleanup(identity.ResetForTest)
}

func decodeClaims(t *testing.T, jwt string) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("expected compact JWT, got %d segments", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

// The handler needs an authenticated principal; the middleware is bypassed
// here because the JWT round trip is covered by the identity package's own
// tests.
func appTokenRequest(t *testing.T, h *Handler, app string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/app-token?app="+app, nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   7,
		TenantID: "default",
		Username: "halfking",
		Role:     "super_admin",
		IsJWT:    true,
	})
	rec := httptest.NewRecorder()
	h.handleAppToken(rec, req)
	return rec
}

func TestHandleAppToken_MintsAudienceScopedTicket(t *testing.T) {
	withMeshSecret(t)
	h := &Handler{}

	rec := appTokenRequest(t, h, "freellmapi")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	token, _ := payload["token"].(string)
	if token == "" {
		t.Fatal("response carried no token")
	}
	if payload["audience"] != "freellmapi-api" {
		t.Fatalf("audience = %v, want freellmapi-api", payload["audience"])
	}

	claims := decodeClaims(t, token)
	if claims["iss"] != "llm-gateway" {
		t.Fatalf("iss = %v, want llm-gateway", claims["iss"])
	}
	if claims["aud"] != "freellmapi-api" {
		t.Fatalf("aud = %v, want freellmapi-api", claims["aud"])
	}
	if claims["username"] != "halfking" {
		t.Fatalf("username = %v, want halfking", claims["username"])
	}
	if claims["user_id"] != "7" {
		t.Fatalf("user_id = %v, want 7", claims["user_id"])
	}

	// The lifetime must be short: a leaked URL is worthless after minutes.
	exp, _ := claims["exp"].(float64)
	iat, _ := claims["iat"].(float64)
	if exp-iat > 600 {
		t.Fatalf("token lifetime = %vs, want <= 600s", exp-iat)
	}
	if exp < float64(time.Now().Unix()) {
		t.Fatalf("token already expired: exp=%v now=%v", exp, time.Now().Unix())
	}

	// The header copy exists so an nginx auth_request can capture the token
	// without parsing the body.
	if got := rec.Header().Get("X-LLMGW-App-Token"); got != token {
		t.Fatalf("X-LLMGW-App-Token = %q, want the token", got)
	}
}

func TestHandleAppToken_UnknownAppIsRejected(t *testing.T) {
	withMeshSecret(t)
	h := &Handler{}

	rec := appTokenRequest(t, h, "not-an-app")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestHandleAppToken_MissingAppIsRejected(t *testing.T) {
	withMeshSecret(t)
	h := &Handler{}

	rec := appTokenRequest(t, h, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// Fail-closed: no shared secret ⇒ no ticket. The peer would refuse it
// anyway, but the gateway must not pretend the feature works.
func TestHandleAppToken_FailsClosedWithoutSharedSecret(t *testing.T) {
	t.Setenv(identity.EnvSharedSecret, "")
	identity.ResetForTest()
	t.Cleanup(identity.ResetForTest)

	h := &Handler{}
	rec := appTokenRequest(t, h, "freellmapi")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "token") && !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("error body should explain the missing configuration, got %s", rec.Body.String())
	}
}

func TestHandleAppToken_RejectsNonGET(t *testing.T) {
	withMeshSecret(t)
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/app-token?app=freellmapi", nil)
	req = SetAuthContext(req, &AuthContext{UserID: 1, Username: "u"})
	rec := httptest.NewRecorder()
	h.handleAppToken(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// stubAppTokenAuditWriter swaps the mint-audit seam and returns a restore
// func (same shape as stubRevocationProbe): settings.WriteAudit needs a live
// *pgxpool.Pool, which admin unit tests cannot inject into Handler.db.
func stubAppTokenAuditWriter(fn func(context.Context, *pgxpool.Pool, settings.AuditEntry)) func() {
	old := appTokenAuditWriter
	appTokenAuditWriter = fn
	return func() { appTokenAuditWriter = old }
}

// F-6: a minted ticket is a credential the peer will exchange for an
// identity, so the success path must leave exactly one audit row naming the
// actor, the tenant and the target app (audience).
func TestHandleAppToken_MintWritesAuditEntry(t *testing.T) {
	withMeshSecret(t)

	var captured []settings.AuditEntry
	defer stubAppTokenAuditWriter(func(_ context.Context, _ *pgxpool.Pool, e settings.AuditEntry) {
		captured = append(captured, e)
	})()

	h := &Handler{}
	rec := appTokenRequest(t, h, "freellmapi")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(captured) != 1 {
		t.Fatalf("audit writes = %d, want exactly 1 on the success path", len(captured))
	}
	e := captured[0]
	if e.SettingKey != "app_token:freellmapi-api" {
		t.Fatalf("setting_key = %q, want app_token:freellmapi-api (target app = audience)", e.SettingKey)
	}
	if e.Action != "mint" {
		t.Fatalf("action = %q, want mint", e.Action)
	}
	if e.TenantID != "default" {
		t.Fatalf("tenant_id = %q, want default", e.TenantID)
	}
	if e.OperatorUser != "halfking" || e.OperatorRole != "super_admin" {
		t.Fatalf("operator = %q/%q, want halfking/super_admin", e.OperatorUser, e.OperatorRole)
	}
	var detail struct {
		UserID int `json:"user_id"`
	}
	if err := json.Unmarshal(e.NewValue, &detail); err != nil {
		t.Fatalf("new_value = %s: %v", e.NewValue, err)
	}
	if detail.UserID != 7 {
		t.Fatalf("user_id = %d, want 7", detail.UserID)
	}
}

// The rejection paths (unknown app 404, unset shared secret 503) are
// read-only failures: they must not produce audit rows. The success-path
// write is pinned by TestHandleAppToken_MintWritesAuditEntry.
func TestHandleAppToken_FailurePathsWriteNoAudit(t *testing.T) {
	withMeshSecret(t)

	var writes int
	defer stubAppTokenAuditWriter(func(_ context.Context, _ *pgxpool.Pool, _ settings.AuditEntry) {
		writes++
	})()

	h := &Handler{}
	if rec := appTokenRequest(t, h, "not-an-app"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	t.Setenv(identity.EnvSharedSecret, "")
	identity.ResetForTest()
	if rec := appTokenRequest(t, h, "freellmapi"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if writes != 0 {
		t.Fatalf("audit writes = %d, want 0 (read-only rejections must not be recorded)", writes)
	}
}
