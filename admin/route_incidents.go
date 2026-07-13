// Package admin — route_incidents.go
//
// Read-only API surface for the dashboard's "diagnose" entry.
//
// Phase 1 (this file) is read-only:
//
//	GET /api/admin/route-incidents
//	GET /api/admin/route-incidents/{id}
//	GET /api/admin/route-incidents/{id}/events
//	GET /api/admin/route-incidents/{id}/timeline
//	GET /api/admin/route-incidents/stats
//
// All endpoints are super-admin only and tenant-filtered. Cross-
// tenant resource access returns 404, never 403, so existence is
// not leaked.
//
// The handlers are deliberately small. Heavy lifting (state
// transitions, event persistence, redaction) lives in
// domains/routeincident. Here we just glue the HTTP layer onto it.
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/routeincident"
)

// RouteIncidentsHandler serves the read-only API. The store is the
// only collaborator. The handler never mutates state.
type RouteIncidentsHandler struct {
	store *routeincident.Store
	db    *pgxpool.Pool
}

// NewRouteIncidentsHandler constructs the handler. Returns nil if
// the store is nil (e.g. the gateway is running without a DB and
// the operator is not using the diagnose feature).
func NewRouteIncidentsHandler(store *routeincident.Store, db *pgxpool.Pool) *RouteIncidentsHandler {
	if store == nil {
		return nil
	}
	return &RouteIncidentsHandler{store: store, db: db}
}

// RegisterRoutes wires the endpoints into the admin mux. All
// endpoints are super-admin only.
func (h *RouteIncidentsHandler) RegisterRoutes(mux *http.ServeMux, superAdmin func(http.HandlerFunc) http.HandlerFunc) {
	if h == nil {
		return
	}
	if superAdmin == nil {
		superAdmin = func(fn http.HandlerFunc) http.HandlerFunc { return fn }
	}
	mux.HandleFunc("/api/admin/route-incidents", superAdmin(h.handleList))
	mux.HandleFunc("/api/admin/route-incidents/stats", superAdmin(h.handleStats))
	mux.HandleFunc("/api/admin/route-incidents/", superAdmin(h.handleSubrouter))
}

// handleList responds to GET /api/admin/route-incidents.
//
// Query parameters:
//
//	state     — "active" | "recovering" | "recovered" (optional)
//	visible   — "1" to exclude recovered (optional)
//	limit     — page size 1..500 (default 100)
//
// Super-admin: tenant_id="" returns all tenants. Tenant-admin calls
// are never expected to reach this handler (superAdmin wrapper
// rejects them) but the handler also accepts a tenant_id query
// parameter for completeness — when set, the result is filtered.
func (h *RouteIncidentsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	filter := routeincident.ListFilter{
		State:       strings.TrimSpace(q.Get("state")),
		OnlyVisible: q.Get("visible") == "1",
	}
	if l := parseIntDefault(q.Get("limit"), 0); l > 0 {
		filter.Limit = l
	}
	// Cross-tenant filtering: super-admin sees everything unless
	// they pin a tenant_id via query. Tenant-admin is rejected by
	// the superAdmin wrapper, so we don't worry about scope here.
	if tid := strings.TrimSpace(q.Get("tenant_id")); tid != "" {
		filter.TenantID = tid
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := h.store.List(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list incidents failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// handleSubrouter dispatches /api/admin/route-incidents/{id}... to
// detail / events / timeline based on the trailing path.
func (h *RouteIncidentsHandler) handleSubrouter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/admin/route-incidents/")
	rest = strings.Trim(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if r.Method == http.MethodGet {
			h.handleDetail(w, r, id)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	switch parts[1] {
	case "events":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleEvents(w, r, id)
	case "timeline":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleTimeline(w, r, id)
	case "audit":
		h.handleAudit(w, r, id)
	case "runs":
		h.handleRuns(w, r, id)
	case "recover", "reprobe", "release-slot", "reset-slots",
		"reset-availability", "direct-upstream-test", "through-gateway-test":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleAction(w, r, id, parts[1])
	case "export":
		h.handleExport(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// handleDetail responds to GET /api/admin/route-incidents/{id}.
// Returns 404 on cross-tenant or missing — the caller cannot tell
// the difference.
func (h *RouteIncidentsHandler) handleDetail(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	inc, err := h.store.Get(ctx, tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get incident failed")
		return
	}
	if inc == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	detail := h.buildDetail(ctx, inc)
	writeJSON(w, http.StatusOK, detail)
}

// handleEvents responds to GET /api/admin/route-incidents/{id}/events
//
// Query parameters:
//
//	limit — page size 1..1000 (default 200)
func (h *RouteIncidentsHandler) handleEvents(w http.ResponseWriter, r *http.Request, id string) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	limit := parseIntDefault(r.URL.Query().Get("limit"), 200)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	events, err := h.store.Events(ctx, tenantID, id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list events failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": events,
		"count": len(events),
	})
}

// handleTimeline responds to GET /api/admin/route-incidents/{id}/timeline.
//
// Returns 5-minute buckets for the last 24h derived from
// request_logs. The store's Timeline24h returns an empty slice when
// the incident has no traffic in the window; the API surfaces that
// as { items: [], insufficient_data: ["timeline_24h"] } so the
// drawer can render an "insufficient data" banner.
func (h *RouteIncidentsHandler) handleTimeline(w http.ResponseWriter, r *http.Request, id string) {
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	_ = from
	_ = to // phase 1 always returns 24h

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	points, err := h.store.Timeline24h(ctx, tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "timeline query failed")
		return
	}
	insufficient := []string{}
	if len(points) == 0 {
		insufficient = append(insufficient, "timeline_24h")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":             points,
		"count":             len(points),
		"insufficient_data": insufficient,
	})
}

// handleAudit responds to GET /api/admin/route-incidents/{id}/audit.
// Phase-2 read endpoint that returns the immutable audit log for
// the incident. Tenant-scoped; cross-tenant returns 404 (the
// handler cannot tell missing-vs-cross-tenant apart by design).
func (h *RouteIncidentsHandler) handleAudit(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// Verify the incident is in this tenant (404 for cross-tenant).
	if inc, err := h.store.Get(ctx, tenantID, id); err != nil || inc == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	entries, err := h.store.AuditLogList(ctx, tenantID, id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": entries,
		"count": len(entries),
	})
}

// handleRuns responds to GET /api/admin/route-incidents/{id}/runs.
// Phase-2 read endpoint that lists diagnostic runs (tests and
// actions) recorded against this incident.
func (h *RouteIncidentsHandler) handleRuns(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if inc, err := h.store.Get(ctx, tenantID, id); err != nil || inc == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	runs, err := h.store.DiagnosticRunsList(ctx, tenantID, id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "runs list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": runs,
		"count": len(runs),
	})
}

// actionNameMap maps URL slugs to the canonical ActionKind.
var actionNameMap = map[string]routeincident.ActionKind{
	"recover":              routeincident.ActionRecover,
	"reprobe":              routeincident.ActionReprobe,
	"release-slot":         routeincident.ActionReleaseSlot,
	"reset-slots":          routeincident.ActionResetSlots,
	"reset-availability":   routeincident.ActionResetAvailability,
	"direct-upstream-test": routeincident.ActionDirectUpstreamTest,
	"through-gateway-test": routeincident.ActionThroughGatewayTest,
}

// handleAction is the dispatch endpoint for every mutating action
// and diagnostic test. The request body is the canonical
// ActionRequest (see domains/routeincident/actions.go). The
// response is ActionResponse. Authorisation is the superAdmin
// middleware (already applied by RegisterRoutes).
//
// Each call records a routing_audit_log row. The unique index on
// `idempotency_key` rejects duplicate executions; the dispatcher
// reuses the cached result on retry.
func (h *RouteIncidentsHandler) handleAction(w http.ResponseWriter, r *http.Request, id, actionSlug string) {
	kind, ok := actionNameMap[actionSlug]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown action")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	var body routeincident.ActionRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	actor, ipHash := actorFromRequest(r)
	expectedVersion := parseInt64Default(r.URL.Query().Get("expected_version"), 0)

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	resp, err := dispatchActionByKind(ctx, h.store, kind, tenantID, id, body, actor, ipHash, expectedVersion)
	if err != nil {
		switch {
		case errors.Is(err, routeincident.ErrStaleState):
			writeError(w, http.StatusConflict, "stale incident state, refetch and retry")
		case errors.Is(err, routeincident.ErrInvalidInput):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, routeincident.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency key conflict")
		case errors.Is(err, routeincident.ErrNoDatabase):
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
		default:
			writeError(w, http.StatusInternalServerError, "action failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// dispatchActionByKind centralises the kind-to-dispatcher routing
// so the handler stays a thin wrapper. The store methods are
// defined in actions_exec.go.
func dispatchActionByKind(
	ctx context.Context,
	store *routeincident.Store,
	kind routeincident.ActionKind,
	tenantID, incidentID string,
	body routeincident.ActionRequest,
	actor, ipHash string,
	expectedVersion int64,
) (*routeincident.ActionResponse, error) {
	switch kind {
	case routeincident.ActionRecover:
		return store.DispatchRecover(ctx, tenantID, incidentID, actor, body.Reason, body.ConfirmationToken, body.IdempotencyKey, ipHash, expectedVersion)
	case routeincident.ActionReprobe:
		return store.DispatchReprobe(ctx, tenantID, incidentID, actor, body.Reason, body.ConfirmationToken, body.IdempotencyKey, ipHash, expectedVersion, body.Parameters)
	case routeincident.ActionReleaseSlot:
		return store.DispatchReleaseSlot(ctx, tenantID, incidentID, actor, body.Reason, body.ConfirmationToken, body.IdempotencyKey, ipHash, expectedVersion, body.Parameters)
	case routeincident.ActionResetSlots:
		return store.DispatchResetSlots(ctx, tenantID, incidentID, actor, body.Reason, body.ConfirmationToken, body.IdempotencyKey, ipHash, expectedVersion, body.Parameters)
	case routeincident.ActionResetAvailability:
		return store.DispatchResetAvailability(ctx, tenantID, incidentID, actor, body.Reason, body.ConfirmationToken, body.IdempotencyKey, ipHash, expectedVersion, body.Parameters)
	case routeincident.ActionDirectUpstreamTest:
		return store.DispatchDirectUpstreamTest(ctx, tenantID, incidentID, actor, body.Reason, body.ConfirmationToken, body.IdempotencyKey, ipHash, expectedVersion, body.Parameters)
	case routeincident.ActionThroughGatewayTest:
		return store.DispatchThroughGatewayTest(ctx, tenantID, incidentID, actor, body.Reason, body.ConfirmationToken, body.IdempotencyKey, ipHash, expectedVersion, body.Parameters)
	default:
		return nil, fmt.Errorf("%w: unsupported action kind %q", routeincident.ErrInvalidInput, kind)
	}
}

// handleExport responds to GET /api/admin/route-incidents/{id}/export?run_id=...
// Phase-2 evidence export. Builds the sanitized bundle, writes the
// audit row, and returns the bundle with an integrity checksum.
func (h *RouteIncidentsHandler) handleExport(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run_id is required")
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	confirmToken := strings.TrimSpace(r.URL.Query().Get("confirmation_token"))
	idemKey := strings.TrimSpace(r.URL.Query().Get("idempotency_key"))
	if idemKey == "" {
		// The dashboard always supplies one; we fall back to a
		// deterministic key so a manual download doesn't double-
		// audit. Production deployments are expected to pass an
		// explicit idempotency_key from the client.
		idemKey = "export-" + runID
	}
	actor, ipHash := actorFromRequest(r)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Confirm the incident belongs to this tenant; otherwise the
	// export would leak cross-tenant data.
	if inc, err := h.store.Get(ctx, tenantID, id); err != nil || inc == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	export, err := h.store.BuildEvidenceExport(ctx, tenantID, runID, actor)
	if err != nil {
		if errors.Is(err, routeincident.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "export failed")
		return
	}
	if export == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if err := h.store.RecordEvidenceExportAudit(ctx, tenantID, id, actor, reason, confirmToken, idemKey, ipHash, runID); err != nil {
		// Audit failure must not leak the export — return 500 so
		// the operator re-tries with an idempotent key.
		writeError(w, http.StatusInternalServerError, "audit failed")
		return
	}

	writeJSON(w, http.StatusOK, export)
}

// handleStats responds to GET /api/admin/route-incidents/stats.
// Phase 1 returns the observer's queue counters; the operator can
// tell at a glance whether events are being dropped.
func (h *RouteIncidentsHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h == nil || h.store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false})
		return
	}
	// We don't have a direct handle to the observer from the
	// handler; the wiring is one-directional. The stats endpoint
	// reports store-level counters instead, which is what the
	// operator cares about in phase 1.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	counts := h.storeCounters(ctx)
	writeJSON(w, http.StatusOK, counts)
}

func (h *RouteIncidentsHandler) storeCounters(ctx context.Context) map[string]any {
	out := map[string]any{
		"active":        0,
		"recovering":    0,
		"recovered_24h": 0,
	}
	if h.store == nil {
		return out
	}
	active, _ := h.store.List(ctx, routeincident.ListFilter{State: "active", Limit: 1})
	recovering, _ := h.store.List(ctx, routeincident.ListFilter{State: "recovering", Limit: 1})
	out["active"] = len(active)
	out["recovering"] = len(recovering)
	return out
}

// buildDetail assembles the rich detail payload for the drawer.
// Phase 1 returns the current snapshot + a 24h timeline. Phase 2
// will fill in the resource snapshot and request-comparison
// sections from additional tables.
func (h *RouteIncidentsHandler) buildDetail(ctx context.Context, inc *routeincident.Incident) routeincident.IncidentDetail {
	detail := routeincident.IncidentDetail{
		Incident: *inc,
		RecoveryProgress: routeincident.RecoveryProgress{
			Current: inc.RecoveryStreak,
			Target:  routeincident.DefaultThresholds().SuccessToRecovered,
		},
		ResourceSnapshot: routeincident.ResourceSnapshot{
			ActionHistoryCount: 0, // phase 1 has no operator actions
		},
		Findings:         h.findingsFor(ctx, inc),
		SampleRequests:   h.sampleRequestsFor(ctx, inc),
		InsufficientData: nil,
	}

	if pts, err := h.store.Timeline24h(ctx, inc.RouteKey.TenantID, inc.ID); err == nil {
		detail.Timeline = pts
		if len(pts) == 0 {
			detail.InsufficientData = append(detail.InsufficientData, "timeline_24h")
		}
	}

	// Current route is a snapshot of the route identity. The drawer
	// uses it to render "what is this route?". Phase 2 will hydrate
	// routing decision and retry path from the decision trace.
	detail.CurrentRoute = routeincident.RouteSnapshot{
		Protocol:        inc.RouteKey.Protocol,
		CanonicalModel:  inc.RouteKey.Model,
		OutboundModel:   inc.RouteKey.Model,
		ProviderID:      inc.RouteKey.ProviderID,
		CredentialID:    inc.RouteKey.CredentialID,
		RoutingDecision: "primary",
	}
	return detail
}

// findingsFor computes a small set of evidence-backed findings for
// the drawer's "Evidence summary" section. Phase 1 includes:
//   - dominant error kind
//   - failure stage distribution
//
// Each finding carries the sample count, interval, and a small list
// of request IDs that backed it. Insufficient data is returned in
// the detail's InsufficientData slice so the API never infers a
// root cause from missing evidence.
func (h *RouteIncidentsHandler) findingsFor(ctx context.Context, inc *routeincident.Incident) []routeincident.IncidentFinding {
	out := []routeincident.IncidentFinding{}
	if h.db == nil {
		return out
	}
	// Pull a small bounded set of recent failures for this route
	// from request_logs. The query is tenant-scoped on tenant_id
	// so cross-tenant reads are impossible.
	const q = `
		SELECT rl.error_kind, rl.failure_stage, rl.request_id
		FROM request_logs_with_current_month rl
		WHERE rl.tenant_id = $1
		  AND rl.success = FALSE
		  AND rl.ts >= $2
		  AND (rl.outbound_model = $3 OR rl.client_model = $3)
		  AND COALESCE(rl.provider_id, 0) = COALESCE($4, 0)
		  AND COALESCE(rl.credential_id, 0) = COALESCE($5, 0)
		ORDER BY rl.ts DESC
		LIMIT 32
	`
	rows, err := h.db.Query(ctx, q,
		inc.RouteKey.TenantID,
		inc.FirstFailureAt,
		inc.RouteKey.Model,
		inc.RouteKey.ProviderID,
		inc.RouteKey.CredentialID,
	)
	if err != nil {
		return out
	}
	defer rows.Close()
	type bucket struct {
		kind  string
		stage string
		ids   []string
	}
	byKey := map[string]*bucket{}
	total := 0
	for rows.Next() {
		var kind, stage *string
		var reqID string
		if err := rows.Scan(&kind, &stage, &reqID); err != nil {
			continue
		}
		total++
		k := ""
		if kind != nil {
			k = *kind
		}
		s := ""
		if stage != nil {
			s = *stage
		}
		key := k + "|" + s
		b, ok := byKey[key]
		if !ok {
			b = &bucket{kind: k, stage: s}
			byKey[key] = b
		}
		if len(b.ids) < 5 {
			b.ids = append(b.ids, reqID)
		}
	}
	if total == 0 {
		return out
	}
	for _, b := range byKey {
		out = append(out, routeincident.IncidentFinding{
			Kind:        "error_kind_rate",
			Label:       composeFindingLabel(b.kind, b.stage, len(b.ids), total),
			SampleCount: len(b.ids),
			IntervalSec: int(time.Since(inc.FirstFailureAt).Seconds()),
			RequestIDs:  b.ids,
			Note:        "Derived from terminal request_logs rows in the failure window. Sample ids are bounded; the dashboard does not re-aggregate to avoid drift.",
		})
	}
	return out
}

func composeFindingLabel(kind, stage string, count, total int) string {
	if kind == "" && stage == "" {
		return "未分类失败"
	}
	if kind == "" {
		return stage + " 失败 × " + riItoa(count) + " / " + riItoa(total)
	}
	if stage == "" {
		return kind + " × " + riItoa(count) + " / " + riItoa(total)
	}
	return kind + " (" + stage + ") × " + riItoa(count) + " / " + riItoa(total)
}

// riItoa is a tiny int-to-string helper scoped to this file so it
// does not collide with the same name in the rest of the admin
// package. Not used outside the findingsFor path.
func riItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// actorFromRequest extracts the authenticated user id from the
// request's AuthContext (set by AdminMiddleware) plus the IP hash
// for the audit row. We never store the raw IP — only its SHA-256
// prefix. If the middleware didn't populate the user id (which
// would be a programming error since the route is super-admin
// only), we fall back to "unknown" so the audit row is still
// attributed.
func actorFromRequest(r *http.Request) (actor, ipHash string) {
	actor = "unknown"
	if auth := GetAuthContext(r); auth != nil {
		if auth.Username != "" {
			actor = auth.Username
		} else if auth.UserID != 0 {
			actor = fmt.Sprintf("user-%d", auth.UserID)
		}
	}
	if v, ok := r.Context().Value(adminIPKey{}).(string); ok && v != "" {
		ipHash = ipShortHash(v)
	}
	if ipHash == "" {
		ipHash = ipShortHash(r.RemoteAddr)
	}
	return actor, ipHash
}

// adminIPKey is the request-context key for the caller IP. The
// middleware that resolves a request typically puts the IP
// directly in r.RemoteAddr, but a downstream proxy may populate
// this context value to give the audit row a hashable source.
// adminUserKey is unused — the user id comes from AuthContext
// via the standard AdminMiddleware.
type adminIPKey struct{}

// ipShortHash is a short SHA-256 prefix (16 hex chars) — same
// scheme as the domain layer's hashIP helper. We duplicate the
// tiny implementation here rather than importing it to keep the
// admin layer free of crypto dependencies.
func ipShortHash(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(s))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// parseInt64Default is the int64 variant of parseIntDefault. The
// default is returned for any malformed input.
func parseInt64Default(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n := int64(0)
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int64(c-'0')
		if n > 1<<62 {
			return def
		}
	}
	return n
}

// sampleRequestsFor returns a small list of request IDs that
// contributed to the current incident. Phase 1 just walks the
// event trail; phase 2 will rank by recency / latency / error
// pattern.
func (h *RouteIncidentsHandler) sampleRequestsFor(ctx context.Context, inc *routeincident.Incident) []string {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT request_id FROM route_incident_events
		WHERE incident_id = $1 AND request_id IS NOT NULL
		ORDER BY created_at DESC
		LIMIT 8
	`, inc.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// ─── helpers ────────────────────────────────────────────────────────
//
// writeJSON / writeError / queryInt are defined in handler.go; this
// file reuses them. We keep a small `parseIntDefault` here for the
// cases where we want to read a query param defensively without
// touching the global helper.

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return def
		}
	}
	return n
}
