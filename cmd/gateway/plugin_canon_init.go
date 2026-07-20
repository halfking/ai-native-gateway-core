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

// CanonHandlers bundles admin handlers exposed via canonical plugin routes.
// Keeping them in a struct (rather than a growing positional arg list) lets
// new endpoints be added without touching every existing call site / test.
//
// Field semantics (each is an admin handler wired up in main.go):
//
//	List       → HandleSessionAnalyticsList  (GET /sessions)
//	Detail     → HandleSessionAnalyticsDetail (GET /sessions/{id}, path-rewrite)
//	Turns      → SessionCompareAPI.HandleCompare (GET /sessions/{id}/turns, ?session_id=)
//	Panorama   → HandleSessionPanorama        (GET /sessions/{id}/panorama, path-rewrite)
//	Breakdown  → HandleModelBreakdown         (GET /analytics/breakdown, query-passthrough)
//	Timeseries → HandleCostTrend              (GET /analytics/timeseries, query-passthrough)
//	Top        → HandleTopSessions            (GET /analytics/top, query-passthrough)
//	Clusters   → HandleSessionClustersList    (GET /analytics/clusters, query-passthrough)
type CanonHandlers struct {
	List       http.HandlerFunc
	Detail     http.HandlerFunc
	Turns      http.HandlerFunc
	Panorama   http.HandlerFunc
	Breakdown  http.HandlerFunc
	Timeseries http.HandlerFunc
	Top        http.HandlerFunc
	Clusters   http.HandlerFunc
}

// registerPluginCanonRoutes mounts the canonical plugin-facing API under
// /_gateway/plugin/v1/. Currently this exposes:
//
//	GET /_gateway/plugin/v1/sessions                    → h.List
//	GET /_gateway/plugin/v1/sessions/{id}               → h.Detail (path rewritten)
//	GET /_gateway/plugin/v1/sessions/{id}/turns         → h.Turns (id → ?session_id=)
//	GET /_gateway/plugin/v1/sessions/{id}/panorama      → h.Panorama (path rewritten)
//	GET /_gateway/plugin/v1/analytics/{type}            → analytics dispatch
//	    where {type} ∈ {breakdown, timeseries, top, clusters} (query-passthrough)
//
// Auth is the signed plugin context (X-Gateway-Context-Signature HMAC over
// pluginID|tenantID|ts|nonce keyed by secret), NOT an admin cookie. After
// verification the request is handed to the existing admin handlers under a
// tenant-scoped AuthContext, so the plugin reuses the same SQL/logic as the
// browser admin without duplicating it.
//
// Path rewrite for detail/panorama: admin.HandleSessionAnalyticsDetail and
// admin.HandleSessionPanorama parse the gw_session_id via
// pathSegment(r.URL.Path, "/api/admin/session-analytics/", 0), so they expect
// the URL path to begin with that admin prefix. The canonical plugin paths are
// /_gateway/plugin/v1/sessions/{gw_session_id}{,/panorama}; the session
// dispatch below rewrites r.URL.Path to /api/admin/session-analytics/<id> (or
// .../<id>/panorama) before invoking the handler, so pathSegment finds the id.
// The rewrite runs on the *verified* request only (after signature + nonce
// checks have passed), so a forged caller can never reach this branch.
//
// Turns (compare): admin.SessionCompareAPI.HandleCompare reads the target
// session from ?session_id= (not the path) and the tenant via
// EffectiveTenantID(r). The dispatch below sets ?session_id=<id> from the path
// value before invoking h.Turns. Any unknown suffix on /sessions/{id}/{sub}
// is rejected with 404 rather than falling through to detail — this prevents
// an attacker from probing arbitrary sub-resources and keeps the {sub}
// wildcard from accidentally hitting the detail branch.
//
// Analytics (query-passthrough): HandleModelBreakdown / HandleCostTrend /
// HandleTopSessions / HandleSessionClustersList all read query filters + the
// tenant via EffectiveTenantIDAll(r). They take the path-suffix (e.g.
// "breakdown") from the route only as a switch; the actual metric/filters are
// query params, so the dispatch just forwards r unchanged. Unknown {type}
// values 404.
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
func registerPluginCanonRoutes(mux *http.ServeMux, secret []byte, h CanonHandlers, opts ...pluginruntime.CanonOption) {
	// Session dispatch: /sessions, /sessions/{id}, /sessions/{id}/{sub}.
	// The {sub} segment is "turns", "panorama", or "" (detail). Anything else
	// 404s rather than falling through.
	sessionDispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("gw_session_id")
		sub := r.PathValue("sub")
		if id == "" && sub == "" {
			h.List(w, r)
			return
		}
		if id != "" {
			if !isValidSessionID(id) {
				http.Error(w, "invalid session id", http.StatusBadRequest)
				return
			}
			switch sub {
			case "":
				// admin.HandleSessionAnalyticsDetail parses the id via
				// pathSegment expecting the /api/admin/session-analytics/
				// prefix; rewrite so it finds the id.
				// Reached only after VerifyPluginContext + nonce.
				r.URL.Path = "/api/admin/session-analytics/" + id
				h.Detail(w, r)
			case "turns":
				// SessionCompareAPI.HandleCompare reads ?session_id= and
				// EffectiveTenantID(r). We synthesize the query param from the
				// canonical path value so the existing handler needs no changes.
				q := r.URL.Query()
				q.Set("session_id", id)
				r.URL.RawQuery = q.Encode()
				h.Turns(w, r)
			case "panorama":
				// admin.HandleSessionPanorama parses the id via pathSegment
				// expecting the /api/admin/session-analytics/ prefix; rewrite
				// so it finds the id. Reached only after VerifyPluginContext.
				r.URL.Path = "/api/admin/session-analytics/" + id + "/panorama"
				h.Panorama(w, r)
			default:
				// Reject unknown sub-resources; don't fall through to detail.
				http.NotFound(w, r)
			}
			return
		}
		http.NotFound(w, r)
	})

	// Analytics dispatch: /analytics/{type} (query-passthrough). The handlers
	// read filters from query params and tenant via EffectiveTenantIDAll(r);
	// only the path suffix selects which handler runs.
	analyticsDispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("type") {
		case "breakdown":
			h.Breakdown(w, r)
		case "timeseries":
			h.Timeseries(w, r)
		case "top":
			h.Top(w, r)
		case "clusters":
			h.Clusters(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	wrappedSession := pluginruntime.VerifyPluginContext(secret, scopedAdminContext(sessionDispatch), opts...)
	wrappedAnalytics := pluginruntime.VerifyPluginContext(secret, scopedAdminContext(analyticsDispatch), opts...)

	mux.Handle("GET /_gateway/plugin/v1/sessions", wrappedSession)
	mux.Handle("GET /_gateway/plugin/v1/sessions/{gw_session_id}", wrappedSession)
	mux.Handle("GET /_gateway/plugin/v1/sessions/{gw_session_id}/{sub}", wrappedSession)
	mux.Handle("GET /_gateway/plugin/v1/analytics/{type}", wrappedAnalytics)
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
