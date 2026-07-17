package admin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SessionServiceClaims struct {
	TenantID string `json:"tenant_id"`
	Scope    string `json:"scope"`
	jwt.RegisteredClaims
}

func SessionAnalyticsMiddleware(next http.HandlerFunc, db *pgxpool.Pool, adminSecret, serviceSecret, issuer, audience string) http.HandlerFunc {
	adminNext := AdminMiddleware(next, db, adminSecret)
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(serviceSecret) == "" {
			adminNext(w, r)
			return
		}
		tokenString, ok := bearerToken(r)
		if !ok {
			adminNext(w, r)
			return
		}
		claims, err := verifySessionServiceToken(tokenString, serviceSecret, issuer, audience)
		if err != nil {
			adminNext(w, r)
			return
		}
		if claims.TenantID == "" || !hasScope(claims.Scope, "session:read") {
			writeError(w, http.StatusForbidden, "session service scope or tenant missing")
			return
		}
		authReq := SetAuthContext(r, &AuthContext{
			TenantID: claims.TenantID,
			Username: claims.Subject,
			Role:     "tenant_admin",
			IsJWT:    true,
		})
		next(w, authReq)
	}
}

func verifySessionServiceToken(tokenString, secret, issuer, audience string) (*SessionServiceClaims, error) {
	claims := &SessionServiceClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected service token algorithm: %s", token.Method.Alg())
		}
		return []byte(secret), nil
	}, jwt.WithIssuer(issuer), jwt.WithAudience(audience))
	if err != nil {
		return nil, err
	}
	if !token.Valid || claims.Subject == "" {
		return nil, fmt.Errorf("invalid service token claims")
	}
	return claims, nil
}

func hasScope(raw, required string) bool {
	for _, scope := range strings.Fields(raw) {
		if scope == required {
			return true
		}
	}
	return false
}

func bearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	prefix := "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
