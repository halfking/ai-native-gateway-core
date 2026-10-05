package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// queryBoardBackgroundTasks 读看板顶部「后台任务」chip 的数据。
//
// ## 2026-10-03：原来三处 `_ = ...Scan(...)` 丢弃错误
//
// 后果不是「少一块 chip」，而是**主动宣称健康**：
// 前端 BoardOpsBar.vue 渲染 `checks_last_10m ?? 0` 与一个 `ops-dot--ok`
// 绿色状态点。查询失败时页面显示「10 分钟内检查 0 次」+ 绿点 ——
// 「没查出来」被讲成「一切正常」，这比显示 0 更糟。
//
// 现在两处查询各自带 degraded 标记，供前端决定显示什么。
func (h *Handler) queryBoardBackgroundTasks(ctx context.Context) map[string]any {
	var discStatus *string
	var discStarted, discHeartbeat *time.Time
	var discTrigger *string
	// 2026-10-03：记录错误而不是丢弃。非 42P01 同样记录 —— 这个 chip 讲的是
	// 「系统健康吗」，任何查不出来的情况都不该被渲染成绿灯。
	discErr := h.db.QueryRow(ctx, `
		SELECT status, started_at, heartbeat_at, trigger
		FROM model_discovery_runs
		WHERE tenant_id = 'default'
		ORDER BY started_at DESC LIMIT 1
	`).Scan(&discStatus, &discStarted, &discHeartbeat, &discTrigger)
	if discErr != nil && !errors.Is(discErr, pgx.ErrNoRows) {
		slog.Warn("board: discovery run status query failed", "error", discErr)
	}

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
	checksErr := h.db.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE created_at > now() - interval '10 minutes')
		FROM credential_health_checks
	`).Scan(&checksLast10m)
	if checksErr != nil {
		slog.Warn("board: credential health check count query failed", "error", checksErr)
	}

	out := map[string]any{
		"discovery":  discovery,
		"probe_loop": map[string]any{"checks_last_10m": checksLast10m},
	}
	// 恒发：前端要区分「真的 0 次」与「没查出来」。字段缺失与 0 不可分。
	out["degraded"] = discErr != nil || checksErr != nil
	if discErr != nil {
		out["degraded_reason"] = "discovery status unavailable"
	}
	if checksErr != nil {
		out["probe_degraded"] = true
	}
	return out
}

func (h *Handler) queryBoardSelfCheck(ctx context.Context) map[string]any {
	since := time.Now().Add(-24 * time.Hour)
	var total, success int
	var lastStatus *string
	var lastAt *time.Time
	// 2026-10-03：同 queryBoardBackgroundTasks —— 原来丢弃错误，
	// 失败时前端显示「成功率 0.0%」+ 绿点，把「没查出来」讲成「全都不健康」
	// 或者反过来讲成「健康」，取决于用户怎么读那个 0。
	selfErr := h.db.QueryRow(ctx, `
		SELECT COUNT(*),
			COUNT(*) FILTER (WHERE status = 'success'),
			(SELECT status FROM self_check_runs ORDER BY started_at DESC LIMIT 1),
			(SELECT started_at FROM self_check_runs ORDER BY started_at DESC LIMIT 1)
		FROM self_check_runs WHERE started_at >= $1
	`, since).Scan(&total, &success, &lastStatus, &lastAt)
	if selfErr != nil {
		slog.Warn("board: self-check summary query failed", "error", selfErr)
	}

	rate := 0.0
	if total > 0 {
		rate = float64(success) / float64(total)
	}
	out := map[string]any{
		"total_runs_24h": total,
		"success_rate":   rate,
		"last_status":    strPtrVal(lastStatus),
		// 恒发（无 omitempty）：前端要区分「真的 0 次运行」与「没查出来」。
		"degraded": selfErr != nil,
	}
	if selfErr != nil {
		out["degraded_reason"] = "self-check summary unavailable"
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
			warnRowSkip("error drill minute", err)
			continue
		}
		items = append(items, item)
	}
	if dimension == "provider" {
		items = h.resolveProviderPieLabels(ctx, items)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func strPtrVal(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
