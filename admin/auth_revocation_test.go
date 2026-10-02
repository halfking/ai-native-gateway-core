package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fillRevocationCache primes the in-process epoch cache so probe paths can
// run without a database (the pool is never dereferenced while the cache is
// warm).
func fillRevocationCache(t *testing.T, entries map[string]string) *pgxpool.Pool {
	t.Helper()
	raw, _ := json.Marshal(entries)
	parsed := parseAuthRevocations(raw)
	authRevocationMu.Lock()
	authRevocationMap = parsed
	authRevocationLoaded = time.Now()
	authRevocationMu.Unlock()
	t.Cleanup(invalidateAuthRevocationCache)
	return &pgxpool.Pool{}
}

// stubRevocationProbe swaps the middleware-facing probe seam and returns a
// restore func.
func stubRevocationProbe(fn func(ctx context.Context, userID int, issuedAt time.Time) bool) func() {
	old := authRevocationProbe
	authRevocationProbe = func(ctx context.Context, db *pgxpool.Pool, userID int, issuedAt time.Time) bool {
		return fn(ctx, userID, issuedAt)
	}
	return func() { authRevocationProbe = old }
}

// signTokenWithIssuedAt builds a locally-shaped admin JWT with a caller
// controlled iat (SignToken always stamps now).
func signTokenWithIssuedAt(userID int, tenantID, username, role, secretKey string, issuedAt time.Time) (string, time.Time, error) {
	expiresAt := issuedAt.Add(24 * time.Hour)
	claims := JWTClaims{
		UserID:   userID,
		TenantID: tenantID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			Issuer:    "llm-gateway",
			Audience:  jwt.ClaimStrings{AudienceName},
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secretKey))
	return signed, expiresAt, err
}

func TestParseAuthRevocations(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	stale := now.Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	raw, _ := json.Marshal(map[string]string{
		"7":   fresh,
		"8":   stale,
		"bad": "not-a-number-key",
		"9":   "not-a-time",
		"0":   fresh, // zero user id must be dropped
		"-3":  fresh,
	})
	got := parseAuthRevocations(raw)
	if len(got) != 1 {
		t.Fatalf("expected only the fresh entry to survive, got %v", got)
	}
	epoch, ok := got[7]
	if !ok {
		t.Fatal("user 7 epoch missing")
	}
	if want, _ := time.Parse(time.RFC3339Nano, fresh); !epoch.Equal(want) {
		t.Fatalf("epoch mismatch: got %v want %v", epoch, want)
	}
	if parseAuthRevocations(nil) != nil {
		t.Fatal("nil raw must parse to nil map")
	}
	if parseAuthRevocations([]byte("{broken")) != nil {
		t.Fatal("broken JSON must parse to nil map")
	}
}

func TestProbeAuthRevocation(t *testing.T) {
	now := time.Now()
	t.Run("nil db fails open", func(t *testing.T) {
		if probeAuthRevocation(context.Background(), nil, 7, now) {
			t.Fatal("nil db must fail open")
		}
	})
	t.Run("issued before epoch is revoked", func(t *testing.T) {
		pool := fillRevocationCache(t, map[string]string{"7": now.Add(time.Minute).UTC().Format(time.RFC3339Nano)})
		if !probeAuthRevocation(context.Background(), pool, 7, now.Add(-time.Hour)) {
			t.Fatal("token issued before epoch must be revoked")
		}
	})
	t.Run("issued after epoch survives", func(t *testing.T) {
		pool := fillRevocationCache(t, map[string]string{"7": now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)})
		if probeAuthRevocation(context.Background(), pool, 7, now) {
			t.Fatal("token issued after epoch must survive")
		}
	})
	t.Run("unknown user survives", func(t *testing.T) {
		pool := fillRevocationCache(t, map[string]string{"7": now.Add(time.Minute).UTC().Format(time.RFC3339Nano)})
		if probeAuthRevocation(context.Background(), pool, 99, now.Add(-time.Hour)) {
			t.Fatal("user without epoch must survive")
		}
	})
}

// TestAdminMiddleware_RevokedOldTokenGets401 is the B4 acceptance case:
// a token issued before the password change gets 401 from the /api/ auth
// middleware, while a fresh token still passes.
func TestAdminMiddleware_RevokedOldTokenGets401(t *testing.T) {
	// Pin the env secret to the literal key: jwtSecret() prefers
	// LLM_GATEWAY_JWT_SECRET, and other tests in this package set it
	// without cleanup. signTokenWithIssuedAt signs with the raw literal,
	// so both sides must agree on it.
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "test-secret")
	issuedBeforeChange := time.Now().Add(-time.Hour)
	revocationEpoch := time.Now().Add(-time.Minute)

	restore := stubRevocationProbe(func(ctx context.Context, userID int, issuedAt time.Time) bool {
		return issuedAt.Before(revocationEpoch)
	})
	defer restore()

	newReq := func(token string) *httptest.ResponseRecorder {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		mw := AdminMiddleware(next, nil, "test-secret")
		req := httptest.NewRequest("GET", "/api/users", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		mw(rr, req)
		return rr
	}

	oldToken, _, err := signTokenWithIssuedAt(42, "default", "legacy-user", "tenant_admin", "test-secret", issuedBeforeChange)
	if err != nil {
		t.Fatalf("sign old token: %v", err)
	}
	if rr := newReq(oldToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for pre-change token, got %d body=%s", rr.Code, rr.Body.String())
	}

	freshToken, _, err := SignToken(42, "default", "legacy-user", "tenant_admin", "test-secret", false)
	if err != nil {
		t.Fatalf("sign fresh token: %v", err)
	}
	if rr := newReq(freshToken); rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for post-change token, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSuperAdminMiddleware_RevokedOldTokenGets401(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "test-secret")
	revocationEpoch := time.Now().Add(-time.Minute)
	restore := stubRevocationProbe(func(ctx context.Context, userID int, issuedAt time.Time) bool {
		return issuedAt.Before(revocationEpoch)
	})
	defer restore()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := SuperAdminMiddleware(next, nil, "test-secret")

	token, _, err := signTokenWithIssuedAt(1, "default", "admin", "super_admin", "test-secret", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mw(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for pre-change super_admin token, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestVerifyLegacyPropagatesIssuedAt(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "test-secret")
	before := time.Now().Add(-time.Hour)
	token, _, err := signTokenWithIssuedAt(42, "default", "u", "tenant_admin", "test-secret", before)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	claims, err := VerifyLegacy(token, "test-secret")
	if err != nil {
		t.Fatalf("VerifyLegacy: %v", err)
	}
	if claims.IssuedAt.IsZero() {
		t.Fatal("IssuedAt not propagated")
	}
	if diff := claims.IssuedAt.Sub(before); diff > 2*time.Second || diff < -2*time.Second {
		t.Fatalf("IssuedAt drifted: got %v want ~%v", claims.IssuedAt, before)
	}
}
