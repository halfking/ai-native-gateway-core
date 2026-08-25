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
	"github.com/jackc/pgx/v5/pgxpool"
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

type pgBodyReader struct {
	db    *pgxpool.Pool
	fetch bodyFetcher
}

func (r *pgBodyReader) ReadRequestLogsBodies(ctx context.Context, requestID string) (requestdetail.Bodies, requestdetail.Meta, error) {
	meta, err := r.loadRequestLogMeta(ctx, requestID)
	if err != nil {
		return requestdetail.Bodies{}, requestdetail.Meta{}, err
	}
	reqBody, respBody, bodyErr := r.fetch.fetchRequestBodies(ctx, requestID)
	if bodyErr != nil && !errors.Is(bodyErr, sql.ErrNoRows) {
		return requestdetail.Bodies{}, requestdetail.Meta{}, bodyErr
	}
	bodies := requestdetail.Bodies{
		RequestBody:  anyToRaw(reqBody),
		ResponseBody: anyToRaw(respBody),
	}
	outbound, _ := r.loadOutboundBody(ctx, requestID)
	bodies.OutboundBody = outbound
	return bodies, meta, nil
}

func (r *pgBodyReader) loadRequestLogMeta(ctx context.Context, requestID string) (requestdetail.Meta, error) {
	var (
		tenantID    string
		gwSessionID sql.NullString
		gwTaskID    sql.NullString
		clientModel sql.NullString
		status      sql.NullString
		success     sql.NullBool
		latencyMs   sql.NullInt32
	)
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(tenant_id, ''),
		       gw_session_id, gw_task_id, client_model,
		       request_status, success, latency_ms
		  FROM request_logs_hot
		 WHERE request_id = $1
		 ORDER BY ts DESC
		 LIMIT 1
	`, requestID).Scan(&tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	if errors.Is(err, pgx.ErrNoRows) {
		err = r.db.QueryRow(ctx, `
			SELECT COALESCE(tenant_id, ''),
			       gw_session_id, gw_task_id, client_model,
			       request_status, success, latency_ms
			  FROM request_logs_with_current_month
			 WHERE request_id = $1
			 ORDER BY ts DESC
			 LIMIT 1
		`, requestID).Scan(&tenantID, &gwSessionID, &gwTaskID, &clientModel, &status, &success, &latencyMs)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	if err != nil {
		return requestdetail.Meta{}, err
	}
	meta := requestdetail.Meta{RequestID: requestID, TenantID: tenantID}
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
	err := r.db.QueryRow(ctx, `
		SELECT outbound_body::text
		  FROM request_logs_bodies_hot
		 WHERE request_id = $1
		 LIMIT 1
	`, requestID).Scan(&raw)
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

func (r *pgBodyReader) ReadSessionTurnsBodies(ctx context.Context, requestID string) (requestdetail.Bodies, requestdetail.Meta, error) {
	var (
		sessionID     string
		turnNo        int
		requestDelta  []byte
		responseDelta []byte
		outboundBody  []byte
		model         sql.NullString
		latencyMs     sql.NullInt32
	)
	err := r.db.QueryRow(ctx, `
		SELECT t.session_id, t.turn_no,
		       b.request_delta, b.response_delta, b.outbound_body,
		       t.model, t.latency_ms
		  FROM public.session_turns_with_current_month t
		  LEFT JOIN public.session_bodies b
		    ON b.session_id = t.session_id
		   AND b.turn_no = t.turn_no
		   AND b.partition_date = t.partition_date
		 WHERE t.request_id = $1
		 ORDER BY t.ts DESC NULLS LAST
		 LIMIT 1
	`, requestID).Scan(&sessionID, &turnNo, &requestDelta, &responseDelta, &outboundBody, &model, &latencyMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return requestdetail.Bodies{}, requestdetail.Meta{}, requestdetail.ErrNotFound
	}
	if err != nil {
		return requestdetail.Bodies{}, requestdetail.Meta{}, err
	}
	meta := requestdetail.Meta{
		RequestID:   requestID,
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
		return json.RawMessage(t)
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
	if requestID == "" || strings.Contains(requestID, "/") {
		writeError(w, http.StatusBadRequest, "invalid request id")
		return
	}
	omitBody := r.URL.Query().Get("omit_body") == "1" || r.URL.Query().Get("omit_body") == "true"
	detail, err := h.requestDetailLocator.Get(r.Context(), requestID, omitBody)
	if errors.Is(err, requestdetail.ErrNotFound) {
		writeError(w, http.StatusNotFound, "request detail not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Tenant isolation for persisted sources; in-flight is local to this node.
	if detail.Persistence == requestdetail.PersistencePersisted && IsTenantAdmin(r) {
		tenant := GetTenantID(r)
		if detail.Meta.TenantID != "" && detail.Meta.TenantID != tenant {
			writeError(w, http.StatusForbidden, "cross-tenant access denied")
			return
		}
	}
	writeJSON(w, http.StatusOK, detail)
}
