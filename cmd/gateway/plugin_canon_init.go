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
//	GET /_gateway/plugin/v1/sessions              → adminList
//	GET /_gateway/plugin/v1/sessions/{id}         → adminDetail (path rewritten)
//	GET /_gateway/plugin/v1/sessions/{id}/turns   → adminTurns (id → ?session_id=)
//
// Auth is the signed plugin context (X-Gateway-Context-Signature HMAC over
// pluginID|tenantID|ts|nonce keyed by secret), NOT an admin cookie. After
// verification the request is handed to the existing admin handlers
// (HandleSessionAnalyticsList / HandleSessionAnalyticsDetail /
// SessionCompareAPI.HandleCompare) under a tenant-scoped AuthContext, so the
// plugin reuses the same SQL/logic as the browser admin without duplicating it.
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
// Turns (compare): admin.SessionCompareAPI.HandleCompare reads the target
// session from ?session_id= (not the path) and the tenant via
// EffectiveTenantID(r). The dispatch below sets ?session_id=<id> from the path
// value before invoking adminTurns. Any non-"turns" suffix on
// /sessions/{id}/{turns} is rejected with 404 rather than falling through to
// detail — this prevents an attacker from probing arbitrary sub-resources and
// keeps the {turns} wildcard from accidentally hitting the detail branch.
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
func registerPluginCanonRoutes(mux *http.ServeMux, secret []byte, adminList, adminDetail, adminTurns http.HandlerFunc, opts ...pluginruntime.CanonOption) {
	dispatch := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("gw_session_id")
		turns := r.PathValue("turns")
		if id == "" {
			adminList(w, r)
			return
		}
		if !isValidSessionID(id) {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
		if turns != "" {
			if turns != "turns" {
				// Reject unknown sub-resources; don't fall through to detail.
				http.NotFound(w, r)
				return
			}
			// SessionCompareAPI.HandleCompare reads ?session_id= and
			// EffectiveTenantID(r). We synthesize the query param from the
			// canonical path value so the existing handler needs no changes.
			q := r.URL.Query()
			q.Set("session_id", id)
			r.URL.RawQuery = q.Encode()
			adminTurns(w, r)
			return
		}
		// admin.HandleSessionAnalyticsDetail parses the id via pathSegment
		// expecting the /api/admin/session-analytics/ prefix; rewrite so it
		// finds the id. Reached only after VerifyPluginContext + nonce.
		r.URL.Path = "/api/admin/session-analytics/" + id
		adminDetail(w, r)
	}
	wrapped := pluginruntime.VerifyPluginContext(secret, scopedAdminContext(dispatch), opts...)
	mux.Handle("GET /_gateway/plugin/v1/sessions", wrapped)
	mux.Handle("GET /_gateway/plugin/v1/sessions/{gw_session_id}", wrapped)
	mux.Handle("GET /_gateway/plugin/v1/sessions/{gw_session_id}/{turns}", wrapped)
}

// isValidSessionID 只允许字母、数字、下划线、连字符。session id 通常是
// "sess-xxx" / UUID / 数字串，不需要路径分隔符或点号。这是对 admin handler
// pathSegment 解析的 defense-in-depth（SQL 已参数化兜底）。
func isValidSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-':
			// allowed
		default:
			return false
		}
	}
	return true
}
