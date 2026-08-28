// unified_detail.go — GET /api/admin/request-detail/{request_id}
// Unified request detail facade: memory → file → request_logs → session_turns.
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
	h.requestDetailLocator = &requestdetail.Locator{
		Store:  store,
		Bodies: &pgBodyReader{db: h.db, fetch: h},
	}
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
	meta, err := r.loadRequestLogMeta(ctx, requestID)
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
			return requestdetail.Bodies{}, requestdetail.Meta{}, sessionErr
		}
	}
	if len(bodies.RequestBody) == 0 && len(bodies.ResponseBody) == 0 && len(bodies.OutboundBody) == 0 {
		return requestdetail.Bodies{}, meta, requestdetail.ErrNotFound
	}
	return bodies, meta, nil
}

func (r *pgBodyReader) loadRequestLogMeta(ctx context.Context, requestID string) (requestdetail.Meta, error) {
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

	// 2026-08-27 OPTIMIZATION: Split OR into two separate queries for better index usage.
	// The previous OR query prevented efficient index usage. Now we try request_id first
	// (primary key lookup), then client_request_id if not found (indexed lookup).

	// Try request_id first (should be fast - primary key or indexed lookup)
	err := r.db.QueryRow(ctx, `
		SELECT request_id, COALESCE(tenant_id, ''),
		       gw_session_id, gw_task_id, client_model,
		       request_status, success, latency_ms
		  FROM request_logs_hot
		 WHERE request_id = $1
		 LIMIT 1
	`, requestID).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)

	// If not found by request_id, try client_request_id
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT request_id, COALESCE(tenant_id, ''),
			       gw_session_id, gw_task_id, client_model,
			       request_status, success, latency_ms
			  FROM request_logs_hot
			 WHERE client_request_id = $1
			 ORDER BY ts DESC
			 LIMIT 1
		`, requestID).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	}

	// If still not found in hot table, try partitioned table
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT request_id, COALESCE(tenant_id, ''),
			       gw_session_id, gw_task_id, client_model,
			       request_status, success, latency_ms
			  FROM request_logs_with_current_month
			 WHERE request_id = $1
			 LIMIT 1
		`, requestID).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	}

	// Try client_request_id in partitioned table
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT request_id, COALESCE(tenant_id, ''),
			       gw_session_id, gw_task_id, client_model,
			       request_status, success, latency_ms
			  FROM request_logs_with_current_month
			 WHERE client_request_id = $1
			 ORDER BY ts DESC
			 LIMIT 1
		`, requestID).Scan(&canonicalRequestID, &tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	if err != nil {
		return requestdetail.Meta{}, err
	}
	meta := requestdetail.Meta{RequestID: canonicalRequestID, TenantID: tenantID}
	if gwSessionID.Valid {
		meta.GwSessionID = requestdetail.PtrTo(gwSessionID.String)
	}
	if gwTaskID.Valid {
		meta.GwTaskID = requestdetail.PtrTo(gwTaskID.String)
	}
	if clientModel.Valid {
		meta.ClientModel = requestdetail.PtrTo(clientModel.String)
	}
	if status.Valid {
		meta.Status = requestdetail.PtrTo(status.String)
	}
	if success.Valid {
		meta.Success = requestdetail.PtrTo(success.Bool)
	}
	if latencyMs.Valid {
		meta.LatencyMs = requestdetail.PtrTo(int(latencyMs.Int32))
	}
	return meta, nil
}

func (r *pgBodyReader) loadOutboundBody(ctx context.Context, requestID string) (json.RawMessage, error) {
	var raw []byte

	// 2026-08-27 OPTIMIZATION: Try hot table first, then fall back to partitioned table
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
	if omitBody {
		return requestdetail.Bodies{}, requestdetail.Meta{}, requestdetail.ErrNotFound
	}

	err := r.db.QueryRow(ctx, `
		SELECT t.session_id, t.turn_no, t.tenant_id,
		       b.request_delta, b.response_delta, b.outbound_body,
		       t.model, t.latency_ms
		  FROM public.session_turns_with_current_month t
		  LEFT JOIN public.session_bodies b
		    ON b.tenant_id = t.tenant_id
		   AND b.session_id = t.session_id
		   AND b.turn_no = t.turn_no
		   AND b.partition_date = t.partition_date
		 WHERE t.request_id = $1
		 ORDER BY t.ts DESC NULLS LAST
		 LIMIT 1
	`, requestID).Scan(&sessionID, &turnNo, &tenantID, &requestDelta, &responseDelta, &outboundBody, &model, &latencyMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return requestdetail.Bodies{}, requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	if err != nil {
		return requestdetail.Bodies{}, requestdetail.Meta{}, err
	}
	meta := requestdetail.Meta{
		RequestID:   requestID,
		TenantID:    tenantID,
		GwSessionID: requestdetail.PtrTo(sessionID),
		TurnNumber:  requestdetail.PtrTo(turnNo),
	}
	if model.Valid {
		meta.ClientModel = requestdetail.PtrTo(model.String)
	}
	if latencyMs.Valid {
		meta.LatencyMs = requestdetail.PtrTo(int(latencyMs.Int32))
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
	ctx := r.Context()
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 2026-08-26 (P1-29 fix): the previous implementation only checked
	// tenant isolation for PersistencePersisted details, leaving the
	// in-flight / on-disk path (PersistenceInFlight) open to a tenant
	// admin who knew / guessed another tenant's request_id. Apply the
	// same gate to ALL sources: any non-super-admin user may only see
	// details whose TenantID matches their own. An empty TenantID on the
	// detail is treated as "unknown origin" and denied for non-super-admins
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
