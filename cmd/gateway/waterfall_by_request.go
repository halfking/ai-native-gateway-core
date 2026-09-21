// Single-request waterfall lookup for GET …/waterfall/request/{id}.
package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// queryWaterfallByRequestID loads one durable waterfall row by request_id.
func queryWaterfallByRequestID(ctx context.Context, pool *pgxpool.Pool, requestID, tenantID string) (dispatch.WaterfallRequest, bool, error) {
	if pool == nil || requestID == "" {
		return dispatch.WaterfallRequest{}, false, nil
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	const sql = `
SELECT
  request_id,
  COALESCE(tenant_id, ''),
  COALESCE(gw_session_id, ''),
  COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''), NULLIF(canonical_model, ''), ''),
  COALESCE(credential_id, 0),
  CASE WHEN success THEN 'success' ELSE COALESCE(NULLIF(error_kind, ''), 'fail_prefirstbyte') END,
  t0_arrived_at,
  t1_total_enqueued_at,
  t2_total_dequeued_at,
  t3_model_enqueued_at,
  t4_model_dequeued_at,
  t5_cred_enqueued_at,
  t6_cred_dequeued_at,
  t7_forward_start_at,
  t8_response_start_at,
  t9_response_end_at
FROM request_logs_hot
WHERE request_id = $1
  AND ($2 = '' OR tenant_id = $2)
  AND t0_arrived_at IS NOT NULL
LIMIT 1`

	var (
		reqID, tenant, sessionID, mdl, result  string
		cred                                   int64
		t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 *time.Time
	)
	err := pool.QueryRow(qctx, sql, requestID, tenantID).Scan(
		&reqID, &tenant, &sessionID, &mdl, &cred, &result,
		&t0, &t1, &t2, &t3, &t4, &t5, &t6, &t7, &t8, &t9,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dispatch.WaterfallRequest{}, false, nil
		}
		return dispatch.WaterfallRequest{}, false, err
	}
	item := dispatch.WaterfallRequest{
		RequestID:       reqID,
		TenantID:        tenant,
		SessionID:       sessionID,
		Model:           mdl,
		Credential:      int(cred),
		Result:          result,
		ArrivedAt:       formatTimePtr(t0),
		TotalEnqueuedAt: formatTimePtr(t1),
		TotalDequeuedAt: formatTimePtr(t2),
		ModelEnqueuedAt: formatTimePtr(t3),
		ModelDequeuedAt: formatTimePtr(t4),
		CredEnqueuedAt:  formatTimePtr(t5),
		CredDequeuedAt:  formatTimePtr(t6),
		ForwardStartAt:  formatTimePtr(t7),
		ResponseStartAt: formatTimePtr(t8),
		ResponseEndAt:   formatTimePtr(t9),
	}
	item.WaitingInTotalMS = durationMSPtr(t1, t2)
	item.WaitingInModelMS = durationMSPtr(t3, t4)
	item.WaitingInNodeMS = durationMSPtr(t5, t6)
	item.RoutingMS = durationMSPtr(t2, t5)
	item.AcquireMS = durationMSPtr(t6, t7)
	item.UpstreamLatencyMS = durationMSPtr(t7, t8)
	item.StreamingDurationMS = durationMSPtr(t8, t9)
	item.QueueWaitMS = durationMSPtr(t0, t6)
	item.TotalMS = durationMSPtr(t0, t9)
	return item, true, nil
}

// resolveWaterfallByRequest prefers memory ring, then DB.
func resolveWaterfallByRequest(ctx context.Context, mem dispatch.WaterfallRequest, memOK bool, requestID, tenantID string) (dispatch.WaterfallRequest, string, bool) {
	if memOK {
		return mem, "memory", true
	}
	if gatewayDispatchPool == nil {
		return dispatch.WaterfallRequest{}, "none", false
	}
	row, ok, err := queryWaterfallByRequestID(ctx, gatewayDispatchPool, requestID, tenantID)
	if err != nil {
		slog.Warn("dispatch waterfall by-id db lookup failed", "error", err, "request_id", requestID)
		return dispatch.WaterfallRequest{}, "none", false
	}
	if !ok {
		return dispatch.WaterfallRequest{}, "none", false
	}
	return row, "db", true
}
