// Waterfall DB backfill for GET /api/admin/dispatch/waterfall.
//
// Impact matrix (rule 57 — read-only, no DDL):
//
//	structure: request_logs_hot.t0_arrived_at … t9_response_end_at (migration 491)
//	writers:   telemetry RequestLogger INSERT/UPSERT (unchanged)
//	readers:   NEW this file (admin waterfall when memory ring < limit)
//	views:     none touched
//	outbox:    none
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

// gatewayDispatchPool backs durable waterfall samples when the in-memory ring
// is cold (restart / multi-instance). Set once from main during DB init.
var gatewayDispatchPool *pgxpool.Pool

func setGatewayDispatchPool(pool *pgxpool.Pool) {
	gatewayDispatchPool = pool
}

func mergeWaterfallWithDB(ctx context.Context, snap dispatch.WaterfallSnapshot, limit int, model string, credID int, tenantID string) dispatch.WaterfallSnapshot {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	memN := len(snap.Requests)
	if memN >= limit {
		if snap.Source == "" {
			snap.Source = "memory"
		}
		return snap
	}
	if gatewayDispatchPool == nil {
		if snap.Source == "" {
			if memN > 0 {
				snap.Source = "memory"
			} else {
				snap.Source = "none"
			}
		}
		return snap
	}

	need := limit - memN
	seen := make(map[string]struct{}, memN)
	for _, r := range snap.Requests {
		if r.RequestID != "" {
			seen[r.RequestID] = struct{}{}
		}
	}

	dbRows, err := queryWaterfallFromDB(ctx, gatewayDispatchPool, need+memN, model, credID, tenantID)
	if err != nil {
		slog.Warn("dispatch waterfall db backfill failed", "error", err)
		if snap.Source == "" {
			if memN > 0 {
				snap.Source = "memory"
			} else {
				snap.Source = "none"
			}
		}
		return snap
	}

	added := 0
	for _, row := range dbRows {
		if len(snap.Requests) >= limit {
			break
		}
		if _, ok := seen[row.RequestID]; ok {
			continue
		}
		seen[row.RequestID] = struct{}{}
		snap.Requests = append(snap.Requests, row)
		added++
	}

	switch {
	case memN > 0 && added > 0:
		snap.Source = "memory+db"
	case memN > 0:
		snap.Source = "memory"
	case added > 0:
		snap.Source = "db"
	default:
		snap.Source = "none"
	}

	if len(snap.Requests) > 0 {
		end := snap.Requests[0].ResponseEndAt
		if end == "" {
			end = snap.Requests[0].ArrivedAt
		}
		start := snap.Requests[len(snap.Requests)-1].ArrivedAt
		snap.TimeRange = &dispatch.WaterfallTimeRange{Start: start, End: end}
	}
	return snap
}

func queryWaterfallFromDB(ctx context.Context, pool *pgxpool.Pool, limit int, model string, credID int, tenantID string) ([]dispatch.WaterfallRequest, error) {
	if pool == nil || limit <= 0 {
		return nil, nil
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
	WHERE t0_arrived_at IS NOT NULL
	  AND ($1 = '' OR tenant_id = $1)
	  AND ($2 = '' OR outbound_model = $2 OR client_model = $2 OR canonical_model = $2)
	  AND ($3 = 0 OR credential_id = $3)
	ORDER BY t0_arrived_at DESC
	LIMIT $4`

	rows, err := pool.Query(qctx, sql, tenantID, model, credID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]dispatch.WaterfallRequest, 0, limit)
	for rows.Next() {
		var (
			reqID, tenant, sessionID, mdl, result  string
			cred                                   int64
			t0, t1, t2, t3, t4, t5, t6, t7, t8, t9 *time.Time
		)
		if err := rows.Scan(
			&reqID, &tenant, &sessionID, &mdl, &cred, &result,
			&t0, &t1, &t2, &t3, &t4, &t5, &t6, &t7, &t8, &t9,
		); err != nil {
			return nil, err
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
		out = append(out, item)
	}
	return out, rows.Err()
}

func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func durationMSPtr(start, end *time.Time) int {
	if start == nil || end == nil || start.IsZero() || end.IsZero() {
		return 0
	}
	d := end.Sub(*start)
	if d < 0 {
		return 0
	}
	return int(d / time.Millisecond)
}

// mergeWaterfallLists is a pure helper for unit tests (memory-first, dedupe by request_id).
func mergeWaterfallLists(mem, db []dispatch.WaterfallRequest, limit int) (out []dispatch.WaterfallRequest, source string) {
	if limit <= 0 {
		limit = 50
	}
	seen := make(map[string]struct{}, len(mem))
	out = make([]dispatch.WaterfallRequest, 0, limit)
	for _, r := range mem {
		if len(out) >= limit {
			break
		}
		out = append(out, r)
		if r.RequestID != "" {
			seen[r.RequestID] = struct{}{}
		}
	}
	memN := len(out)
	added := 0
	for _, r := range db {
		if len(out) >= limit {
			break
		}
		if r.RequestID != "" {
			if _, ok := seen[r.RequestID]; ok {
				continue
			}
			seen[r.RequestID] = struct{}{}
		}
		out = append(out, r)
		added++
	}
	switch {
	case memN > 0 && added > 0:
		source = "memory+db"
	case memN > 0:
		source = "memory"
	case added > 0:
		source = "db"
	default:
		source = "none"
	}
	return out, source
}
