// Package dashboardapi contains authentication helpers for Dashboard APIs.
package dashboardapi

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey struct{}

var authInfoContextKey contextKey

// GetAuthInfoFromContext returns authentication information injected by admin.
func GetAuthInfoFromContext(ctx context.Context) (AuthInfo, bool) {
	auth, ok := ctx.Value(authInfoContextKey).(AuthInfo)
	return auth, ok
}

// SetAuthInfoToContext injects authentication information for Dashboard handlers.
func SetAuthInfoToContext(ctx context.Context, auth AuthInfo) context.Context {
	return context.WithValue(ctx, authInfoContextKey, auth)
}

func prepareDashboardRequest(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool) (QueryParams, AuthInfo, bool) {
	auth, ok := GetAuthInfoFromContext(r.Context())
	if !ok {
		writeErrorJSON(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", "")
		return QueryParams{}, AuthInfo{}, false
	}
	if db == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, ErrCodeDatabaseError, "database unavailable", "")
		return QueryParams{}, AuthInfo{}, false
	}
	return normalizeDashboardScope(ParseQueryParams(r), auth), auth, true
}

func normalizeDashboardScope(params QueryParams, auth AuthInfo) QueryParams {
	if auth.UserRole != "super_admin" && auth.UserRole != "admin_key" {
		params.TenantID = auth.TenantID
	}
	params.auth = auth
	return params
}

// CheckTenantAccess reports whether auth may request the supplied tenant.
func CheckTenantAccess(auth AuthInfo, requestedTenantID string) bool {
	if auth.UserRole == "super_admin" || auth.UserRole == "admin_key" {
		return true
	}
	return requestedTenantID == "" || auth.TenantID == requestedTenantID
}

// ResolveTenantID returns the effective tenant for a request.
func ResolveTenantID(auth AuthInfo, requestedTenantID string) string {
	if (auth.UserRole == "super_admin" || auth.UserRole == "admin_key") && requestedTenantID != "" {
		return requestedTenantID
	}
	return auth.TenantID
}
