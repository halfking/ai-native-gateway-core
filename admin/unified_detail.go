// unified_detail.go — GET /api/admin/request-detail/{request_id}
// Unified request detail facade: memory → file → request_logs → session_turns.
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
)

// SetRequestDetailStore wires the in-flight content store + locator.
func (h *Handler) SetRequestDetailStore(store *requestdetail.Store) {
	h.requestDetailStore = store
	if store == nil {
		h.requestDetailLocator = nil
		return
	}
	locator := &requestdetail.Locator{Store: store}
	if h.db != nil {
		locator.Bodies = &pgBodyReader{db: h.db, fetch: h}
	}
	h.requestDetailLocator = locator
}

type bodyFetcher interface {
	fetchRequestBodies(ctx context.Context, requestID string) (requestBody, responseBody any, err error)
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type pgBodyReader struct {
	db    queryRower
	fetch bodyFetcher
}

func (r *pgBodyReader) ReadRequestLogsBodies(ctx context.Context, requestID string, omitBody bool) (requestdetail.Bodies, requestdetail.Meta, error) {
	scope := requestdetail.LookupScopeFromContext(ctx)
	meta, err := r.loadRequestLogMeta(ctx, requestID, scope)
	if err != nil {
		return requestdetail.Bodies{}, requestdetail.Meta{}, err
	}
	canonicalRequestID := meta.RequestID
	if omitBody {
		return requestdetail.Bodies{}, meta, nil
	}
	reqBody, respBody, bodyErr := r.fetch.fetchRequestBodies(ctx, canonicalRequestID)
	if bodyErr != nil && !errors.Is(bodyErr, sql.ErrNoRows) {
		return requestdetail.Bodies{}, requestdetail.Meta{}, bodyErr
	}
	if bodyErr != nil {
		if errors.Is(bodyErr, sql.ErrNoRows) {
			// Keep the metadata so Locator can try session_turns before
			// returning a metadata-only detail.
			return requestdetail.Bodies{}, meta, requestdetail.ErrNotFound
		}
		return requestdetail.Bodies{}, requestdetail.Meta{}, bodyErr
	}
	bodies := requestdetail.Bodies{
		RequestBody:  anyToRaw(reqBody),
		ResponseBody: anyToRaw(respBody),
	}
	outbound, outboundErr := r.loadOutboundBody(ctx, canonicalRequestID)
	if outboundErr != nil && !errors.Is(outboundErr, requestdetail.ErrNotFound) {
		return requestdetail.Bodies{}, requestdetail.Meta{}, outboundErr
	}
	bodies.OutboundBody = outbound

	// A persisted body row may be partial (for example, a successful stream
	// with no captured response). Fill only missing fields from session_turns;
	// never replace fields that are already present in request_logs_bodies.
	if len(bodies.RequestBody) == 0 || len(bodies.ResponseBody) == 0 || len(bodies.OutboundBody) == 0 {
		if sessionBodies, _, sessionErr := r.ReadSessionTurnsBodies(ctx, canonicalRequestID, false); sessionErr == nil {
			if len(bodies.RequestBody) == 0 {
				bodies.RequestBody = sessionBodies.RequestBody
			}
			if len(bodies.ResponseBody) == 0 {
				bodies.ResponseBody = sessionBodies.ResponseBody
			}
			if len(bodies.OutboundBody) == 0 {
				bodies.OutboundBody = sessionBodies.OutboundBody
			}
		} else if !errors.Is(sessionErr, requestdetail.ErrNotFound) {
			// A request-log body is already usable; session recovery is an
			// optional completion path and must not discard the primary payload.
			slog.WarnContext(ctx, "requestdetail: session body recovery failed", "request_id", canonicalRequestID, "error", sessionErr)
		}
	}
	if len(bodies.RequestBody) == 0 && len(bodies.ResponseBody) == 0 && len(bodies.OutboundBody) == 0 {
		return requestdetail.Bodies{}, meta, requestdetail.ErrNotFound
	}
	return bodies, meta, nil
}

func (r *pgBodyReader) loadRequestLogMeta(ctx context.Context, requestID string, scope requestdetail.LookupScope) (requestdetail.Meta, error) {
	var (
		canonicalRequestID string
		tenantID           string
		gwSessionID        sql.NullString
		gwTaskID           sql.NullString
		clientModel        sql.NullString
		status             sql.NullString
		success            sql.NullBool
		latencyMs          sql.NullInt32
	)
	tenantClause := ""
	args := []any{requestID}
	if !scope.Unrestricted {
		tenantClause = " AND tenant_id = $2"
		args = append(args, scope.TenantID)
	}

	// 2026-08-28 (audit follow-up, request-detail tenant gate):
	// tenant-scoped lookups MUST be enforced inside SQL, not just at the HTTP
	// handler. Otherwise a tenant_admin can still trip a cross-tenant collision
	// when:
	//   - tenant A and tenant B both wrote a row whose canonical request_id is
	//     shared (rare; usually via shared upstream id) but client_request_id
	//     collides more often because clients control that value.
	//   - the request_id path matches a row owned by another tenant.
	//
	// The HTTP handler still applies its post-fetch 404 gate (defense in depth);
	// SQL-level tenant filtering prevents the wrong row from ever being
	// scanned/serialized on the path.

	// 2026-08-27 OPTIMIZATION: Split OR into two separate queries for better index usage.
	// The previous OR query prevented efficient index usage. Now we try request_id first
	// (primary key lookup), then client_request_id if not found (indexed lookup).

	// Try request_id first (should be fast - primary key or indexed lookup)
	err := r.db.QueryRow(ctx, `
		SELECT request_id, COALESCE(tenant_id, ''),
		       gw_session_id, gw_task_id, client_model,
		       request_status, success, latency_ms
		  FROM request_logs_hot
		 WHERE request_id = $1`+tenantClause+`
		 LIMIT 1
	`, args...).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)

	// If not found by request_id, try client_request_id
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT request_id, COALESCE(tenant_id, ''),
			       gw_session_id, gw_task_id, client_model,
			       request_status, success, latency_ms
			  FROM request_logs_hot
			 WHERE client_request_id = $1`+tenantClause+`
			 ORDER BY ts DESC
			 LIMIT 1
		`, args...).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	}

	// If still not found in hot table, try partitioned table
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT request_id, COALESCE(tenant_id, ''),
			       gw_session_id, gw_task_id, client_model,
			       request_status, success, latency_ms
			  FROM request_logs_with_current_month
			 WHERE request_id = $1`+tenantClause+`
			 LIMIT 1
		`, args...).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	}

	// Try client_request_id in partitioned table
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT request_id, COALESCE(tenant_id, ''),
			       gw_session_id, gw_task_id, client_model,
			       request_status, success, latency_ms
			  FROM request_logs_with_current_month
			 WHERE client_request_id = $1`+tenantClause+`
			 ORDER BY ts DESC
			 LIMIT 1
		`, args...).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	if err != nil {
		return requestdetail.Meta{}, err
	}
	// 2026-08-28 (audit follow-up): when the SQL lookup was tenant-scoped, the
	// returned tenant_id is the row's stored tenant. A non-empty mismatch with
	// the caller's scope would only happen if RLS/role bypass GUCs widened the
	// view — defense in depth: refuse to leak a cross-tenant row.
	if !scope.Unrestricted && (scope.TenantID == "" || tenantID == "" || tenantID != scope.TenantID) {
		return requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	meta := requestdetail.Meta{RequestID: canonicalRequestID, TenantID: tenantID}
	if gwSessionID.Valid {
		v := gwSessionID.String
		meta.GwSessionID = &v
	}
	if gwTaskID.Valid {
		v := gwTaskID.String
		meta.GwTaskID = &v
	}
	if clientModel.Valid {
		v := clientModel.String
		meta.ClientModel = &v
	}
	if status.Valid {
		v := status.String
		meta.Status = &v
	}
	if success.Valid {
		v := success.Bool
		meta.Success = &v
	}
	if latencyMs.Valid {
		v := int(latencyMs.Int32)
		meta.LatencyMs = &v
	}
	return meta, nil
}

func (r *pgBodyReader) loadOutboundBody(ctx context.Context, requestID string) (json.RawMessage, error) {
	var raw []byte

	// Body tables are intentionally tenant-neutral after migration 604. The
	// canonical request ID was resolved through a tenant-scoped metadata query;
	// use that immutable identity for the body lookup rather than duplicating a
	// removed tenant_id column in the body schema.
	// 2026-08-27 OPTIMIZATION: Try hot table first, then fall back to partitioned bodies table
	err := r.db.QueryRow(ctx, `
		SELECT outbound_body::text
		  FROM request_logs_bodies_hot
		 WHERE request_id = $1
		 LIMIT 1
	`, requestID).Scan(&raw)

	// Fallback to partitioned bodies table if not in hot
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT outbound_body::text
			  FROM request_logs_bodies_with_current_month
			 WHERE request_id = $1
			 LIMIT 1
		`, requestID).Scan(&raw)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, requestdetail.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if int64(len(raw)) > requestdetail.MaxBodyFileSize {
		return nil, fmt.Errorf("%w: %d bytes (limit %d)", requestdetail.ErrBodyTooLarge, len(raw), requestdetail.MaxBodyFileSize)
	}
	return json.RawMessage(raw), nil
}

func (r *pgBodyReader) ReadSessionTurnsBodies(ctx context.Context, requestID string, omitBody bool) (requestdetail.Bodies, requestdetail.Meta, error) {
	var (
		sessionID     string
		turnNo        int
		tenantID      string
		requestDelta  []byte
		responseDelta []byte
		outboundBody  []byte
		model         sql.NullString
		latencyMs     sql.NullInt32
	)
	// 2026-08-28 (audit follow-up): session_turns already joins on
	// tenant_id; for restricted lookups, pin t.tenant_id to the caller's scope
	// so a cross-tenant row never enters the result set, then double-check
	// after scan as defense-in-depth.
	scope := requestdetail.LookupScopeFromContext(ctx)
	tenantClause := ""
	tenantArg := ""
	if !scope.Unrestricted {
		tenantClause = " AND t.tenant_id = $2"
		tenantArg = scope.TenantID
	}

	bodyColumns := "NULL::text, NULL::text, NULL::text"
	bodyJoin := ""
	if !omitBody {
		bodyColumns = "b.request_delta, b.response_delta, b.outbound_body"
		bodyJoin = `
		  LEFT JOIN public.session_bodies b
		    ON b.tenant_id = t.tenant_id
		   AND b.session_id = t.session_id
		   AND b.turn_no = t.turn_no
		   AND b.partition_date = t.partition_date`
	}
	args := []any{requestID}
	if !scope.Unrestricted {
		args = append(args, tenantArg)
	}
	err := r.db.QueryRow(ctx, `
		SELECT t.session_id, t.turn_no, t.tenant_id,
		       `+bodyColumns+`,
		       t.model, t.latency_ms
		  FROM public.session_turns_with_current_month t`+bodyJoin+`
		 WHERE t.request_id = $1`+tenantClause+`
		 ORDER BY t.ts DESC NULLS LAST
		 LIMIT 1
	`, args...).Scan(&sessionID, &turnNo, &tenantID, &requestDelta, &responseDelta, &outboundBody, &model, &latencyMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return requestdetail.Bodies{}, requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	if err != nil {
		return requestdetail.Bodies{}, requestdetail.Meta{}, err
	}
	if !scope.Unrestricted && (scope.TenantID == "" || tenantID == "" || tenantID != scope.TenantID) {
		return requestdetail.Bodies{}, requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	meta := requestdetail.Meta{
		RequestID:   requestID,
		TenantID:    tenantID,
		GwSessionID: &sessionID,
		TurnNumber:  &turnNo,
	}
	if model.Valid {
		v := model.String
		meta.ClientModel = &v
	}
	if latencyMs.Valid {
		v := int(latencyMs.Int32)
		meta.LatencyMs = &v
	}
	bodies := requestdetail.Bodies{
		RequestBody:  json.RawMessage(requestDelta),
		ResponseBody: json.RawMessage(responseDelta),
		OutboundBody: json.RawMessage(outboundBody),
	}
	return bodies, meta, nil
}

func anyToRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case json.RawMessage:
		return t
	case []byte:
		return json.RawMessage(t)
	case string:
		return requestdetail.DecodeRaw(&t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		return b
	}
}

func (h *Handler) handleUnifiedRequestDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.requestDetailLocator == nil {
		writeError(w, http.StatusServiceUnavailable, "request detail store not configured")
		return
	}
	prefix := "/api/admin/request-detail/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	requestID := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if err := requestdetail.ValidateRequestID(requestID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request id")
		return
	}
	omitBody := r.URL.Query().Get("omit_body") == "1" || r.URL.Query().Get("omit_body") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ctx = requestdetail.WithLookupScope(ctx, requestdetail.LookupScope{
		TenantID:     GetTenantID(r),
		Unrestricted: IsSuperAdminOrLegacy(r),
	})
	detail, err := h.requestDetailLocator.Get(ctx, requestID, omitBody)
	if errors.Is(err, requestdetail.ErrNotFound) {
		writeError(w, http.StatusNotFound, "request detail not found")
		return
	}
	if err != nil {
		slog.Error("request detail lookup failed", "request_id", requestID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load request detail")
		return
	}
	// 2026-08-26 (P1-29 fix): the previous implementation only checked
	// tenant isolation for PersistencePersisted details, leaving the
	// in-flight / on-disk path (PersistenceInFlight) open to a tenant
	// admin who knew / guessed another tenant's request_id. Apply the
	// same gate to ALL sources: a tenant_admin may only see details
	// whose TenantID matches their own. An empty TenantID on the
	// detail is treated as "unknown origin" and denied for tenant_admins
	// (fail-closed) — legacy in-flight meta written before this commit
	// has no tenant recorded and must not leak across tenants.
	if !IsSuperAdminOrLegacy(r) {
		tenant := GetTenantID(r)
		if detail.Meta.TenantID == "" || detail.Meta.TenantID != tenant {
			writeError(w, http.StatusNotFound, "request detail not found")
			return
		}
	}
	writeJSON(w, http.StatusOK, detail)
}
