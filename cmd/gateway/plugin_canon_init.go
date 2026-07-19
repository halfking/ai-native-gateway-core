package main

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// scopedAdminContext injects an admin AuthContext scoped to the verified
// plugin tenant, so existing admin handlers (which read AuthContext via
// effectiveScopeTenant / ownerScopeClause / IsRegularUser) work without a
// browser cookie.
//
// SECURITY — why Role="tenant_admin" specifically (NOT super_admin):
//
//  1. effectiveScopeTenant(r) returns "" (all tenants) when
//     IsSuperAdminOrLegacy(r) is true, i.e. for Role super_admin/admin_key.
//     Using super_admin here would let a plugin read sessions across ALL
//     tenants — a cross-tenant leak. tenant_admin keeps IsSuperAdminOrLegacy
//     false, so effectiveScopeTenant returns GetTenantID(r), which is the
//     signed/verified tenant from the plugin context. The handler's SQL is
//     then filtered to exactly that tenant.
//
//  2. ownerScopeClause(r,...) only adds an owner_user filter when
//     IsRegularUser(r) is true, and in that branch it dereferences
//     GetAuthContext(r).Username. A plugin carries no JWT username, so if we
//     used a regular-user role the Username would be "" and the clause would
//     emit an unsatisfiable filter (denying all rows) — and worse, any future
//     code path assuming a non-nil Username could nil-deref if AuthContext
//     were ever absent. tenant_admin makes IsRegularUser return false
//     (see admin/session_tenant.go), so no owner filter is applied and
//     GetAuthContext(r) is non-nil here (we just set it), preventing both
//     over-restriction and nil-deref.
//
// Net: the plugin sees exactly the sessions for the tenant it proved it
// belongs to — no more, no less.
func scopedAdminContext(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant := pluginruntime.TenantFromVerified(r)
		r = admin.SetAuthContext(r, &admin.AuthContext{
			Role:     "tenant_admin",
			TenantID: tenant,
			Username: "plugin:" + r.Header.Get("X-Gateway-Plugin-ID"),
		})
		next(w, r)
	}
}

// registerPluginCanonRoutes mounts the canonical plugin-facing API under
// /_gateway/plugin/v1/. Currently this exposes:
//
//	GET /_gateway/plugin/v1/sessions           → adminList
//	GET /_gateway/plugin/v1/sessions/{id}      → adminDetail
//
// Auth is the signed plugin context (X-Gateway-Context-Signature HMAC over
// pluginID|tenantID|ts|nonce keyed by secret), NOT an admin cookie. After
// verification the request is handed to the existing admin handlers
// (HandleSessionAnalyticsList / HandleSessionAnalyticsDetail) under a
// tenant-scoped AuthContext, so the plugin reuses the same SQL/logic as the
// browser admin without duplicating it.
//
// Path rewrite for detail: admin.HandleSessionAnalyticsDetail parses the
// gw_session_id via pathSegment(r.URL.Path, "/api/admin/session-analytics/", 0),
// so it expects the URL path to begin with that admin prefix. The canonical
// plugin path is /_gateway/plugin/v1/sessions/{gw_session_id}; the dispatch
// below rewrites r.URL.Path to /api/admin/session-analytics/<id> before
// invoking adminDetail, so pathSegment finds the id. The rewrite runs on the
// *verified* request only (after signature + nonce checks have passed), so a
// forged caller can never reach this branch.
//
// `opts` are forwarded to VerifyPluginContext. The caller in main.go passes
// WithCanonNonceCache so a token can be used at most once within the cache
// TTL — closing the 5-minute replay window HMAC alone leaves open.
//
// Coupling note: `secret` MUST equal the value the plugin process uses to
// sign its outgoing requests. The plugin side reads
// AI_SESSION_MANAGER_GATEWAY_CONTEXT_SECRET; the gateway reads cfg.SecretKey.
// They must be configured to the same value, or verification will reject
// every plugin call with 401.
func registerPluginCanonRoutes(mux *http.ServeMux, secret []byte, adminList http.HandlerFunc, adminDetail http.HandlerFunc, opts ...pluginruntime.CanonOption) {
	dispatch := func(w http.ResponseWriter, r *http.Request) {
		if id := r.PathValue("gw_session_id"); id != "" {
			// admin.HandleSessionAnalyticsDetail parses the id via pathSegment
			// expecting the /api/admin/session-analytics/ prefix; rewrite so it
			// finds the id. Reached only after VerifyPluginContext + nonce.
			r.URL.Path = "/api/admin/session-analytics/" + id
			adminDetail(w, r)
			return
		}
		adminList(w, r)
	}
	wrapped := pluginruntime.VerifyPluginContext(secret, scopedAdminContext(dispatch), opts...)
	mux.Handle("GET /_gateway/plugin/v1/sessions", wrapped)
	mux.Handle("GET /_gateway/plugin/v1/sessions/{gw_session_id}", wrapped)
}
