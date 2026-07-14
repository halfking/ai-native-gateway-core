package admin

import (
	"context"
	"fmt"
	"time"
)

func (h *Handler) queryBoardBackgroundTasks(ctx context.Context) map[string]any {
	var discStatus *string
	var discStarted, discHeartbeat *time.Time
	var discTrigger *string
	_ = h.db.QueryRow(ctx, `
		SELECT status, started_at, heartbeat_at, trigger
		FROM model_discovery_runs
		WHERE tenant_id = 'default'
		ORDER BY started_at DESC LIMIT 1
	`).Scan(&discStatus, &discStarted, &discHeartbeat, &discTrigger)

	running := discStatus != nil && *discStatus == "running"
	discovery := map[string]any{
		"running": running,
		"status":  strPtrVal(discStatus),
		"trigger": strPtrVal(discTrigger),
	}
	if discStarted != nil {
		discovery["started_at"] = discStarted.UTC().Format(time.RFC3339)
	}
	if discHeartbeat != nil {
		discovery["heartbeat_at"] = discHeartbeat.UTC().Format(time.RFC3339)
	}

	var checksLast10m int
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE created_at > now() - interval '10 minutes')
		FROM credential_health_checks
	`).Scan(&checksLast10m)

	return map[string]any{
		"discovery":  discovery,
		"probe_loop": map[string]any{"checks_last_10m": checksLast10m},
	}
}

func (h *Handler) queryBoardSelfCheck(ctx context.Context) map[string]any {
	since := time.Now().Add(-24 * time.Hour)
	var total, success int
	var lastStatus *string
	var lastAt *time.Time
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*),
			COUNT(*) FILTER (WHERE status = 'success'),
			(SELECT status FROM self_check_runs ORDER BY started_at DESC LIMIT 1),
			(SELECT started_at FROM self_check_runs ORDER BY started_at DESC LIMIT 1)
		FROM self_check_runs WHERE started_at >= $1
	`, since).Scan(&total, &success, &lastStatus, &lastAt)

	rate := 0.0
	if total > 0 {
		rate = float64(success) / float64(total)
	}
	out := map[string]any{
		"total_runs_24h": total,
		"success_rate":   rate,
		"last_status":    strPtrVal(lastStatus),
	}
	if lastAt != nil {
		out["last_run_at"] = lastAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (h *Handler) queryErrorDrill(
	ctx context.Context,
	tenantID string,
	days int,
	errorKind, dimension string,
) ([]boardPieItem, error) {
	items, err := h.queryErrorDrillMinute(ctx, tenantID, days, errorKind, dimension)
	if err == nil && len(items) > 0 {
		return items, nil
	}
	if err != nil && !IsMissingRelationError(err) {
		// fall through to hot-log fallback
	}
	fb, fbErr := h.fallbackErrorDrill(ctx, tenantID, days, errorKind, dimension)
	if fbErr != nil {
		if err != nil {
			return nil, err
		}
		return items, fbErr
	}
	return fb, nil
}

func (h *Handler) queryErrorDrillMinute(
	ctx context.Context,
	tenantID string,
	days int,
	errorKind, dimension string,
) ([]boardPieItem, error) {
	tenantClause, tenantArgs := boardTenantClause(tenantID, 4)
	args := []any{days, errorKind}
	args = append(args, tenantArgs...)

	var groupCol string
	switch dimension {
	case "provider":
		groupCol = "provider_id::text"
	case "client", "client_profile":
		groupCol = "NULLIF(client_profile, '')"
	default:
		groupCol = "NULLIF(model_name, '')"
		dimension = "model"
	}

	rows, err := h.db.Query(ctx, `
		SELECT COALESCE(`+groupCol+`, '__unknown__'),
			COALESCE(SUM(requests), 0),
			0::bigint,
			0::bigint,
			0::float8
		FROM request_stats_error_drill_minute
		WHERE bucket >= now() - ($1::int * INTERVAL '1 day')
		  AND error_kind = $2
		`+tenantClause+`
		GROUP BY 1
		ORDER BY SUM(requests) DESC
		LIMIT 20
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("error drill: %w", err)
	}
	defer rows.Close()

	var items []boardPieItem
	for rows.Next() {
		var item boardPieItem
		if err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func strPtrVal(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
