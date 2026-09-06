package db

import (
	"context"
	"fmt"
	"log/slog"
)

// ensureRequestLogsCurrentMonthView mirrors
// sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql.
//
// 2026-09-07 事故自愈：共享 PG 上的 canonical 查询视图
// request_logs_with_current_month 被带外操作删掉（577/610 的包装视图
// …_without_customer_id、…_without_request_class_due_at 仍在，顶层缺失），
// /api/logs 全部请求 42P01。写路径（telemetry → request_logs_hot）不受影响，
// 只有读路径挂——这类"表在、视图没了"的状态没有任何 migration 会重建
// （610 之后再无 migration 触碰该视图），网关也没有启动自愈，于是持续报错。
//
// 重建按 577 + 610 的包装链分阶段补齐，列契约 = 基础 108 列 UNION +
// customer_id (577) + request_class/due_at (610)。基础列取 hot∩parent 交集
// （排除三个后追加列），因此 hot 的 HOT_ONLY 列（caller_id 等）永远不会
// 撑爆 UNION——这正是 341 式 "SELECT * FROM hot UNION ALL SELECT * FROM
// parent" 重放在 603 之后必失败、进而留下"视图已删未建"的根因。
//
// 编号 SQL 文件供 DBA 同步流程与 deploy-local 修复轨道使用；本函数保证
// 二进制启动即生效（幂等：视图健康时零 DDL）。
func (d *DB) ensureRequestLogsCurrentMonthView(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	var canonicalExists bool
	if err := d.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
		)`,
	).Scan(&canonicalExists); err != nil {
		return fmt.Errorf("probe request_logs_with_current_month existence: %w", err)
	}
	if canonicalExists {
		return nil
	}
	slog.Warn("request_logs_with_current_month missing; self-healing view chain " +
		"(mirror of sql/migrations/startup/679_request_logs_current_month_view_bootstrap.sql)")

	var classWrapperExists, baseWrapperExists bool
	if err := d.pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM pg_views WHERE schemaname = 'public'
				AND viewname = 'request_logs_with_current_month_without_request_class_due_at'),
			EXISTS (SELECT 1 FROM pg_views WHERE schemaname = 'public'
				AND viewname = 'request_logs_with_current_month_without_customer_id')
	`).Scan(&classWrapperExists, &baseWrapperExists); err != nil {
		return fmt.Errorf("probe request_logs view wrappers: %w", err)
	}

	if !baseWrapperExists {
		// 基础 108 列 UNION：hot∩parent 交集（按 hot 列序），排除 575/608
		// 时代后追加的 customer_id/request_class/due_at。
		var baseCols string
		if err := d.pool.QueryRow(ctx, `
			SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
			  FROM pg_attribute h
			  JOIN pg_class hc ON hc.oid = h.attrelid
			  JOIN pg_namespace hn ON hn.oid = hc.relnamespace
			  JOIN pg_attribute p ON p.attrelid = ('public.request_logs')::regclass
			                     AND p.attname = h.attname
			  JOIN pg_class pc ON pc.oid = p.attrelid
			  JOIN pg_namespace pn ON pn.oid = pc.relnamespace
			 WHERE hn.nspname = 'public'
			   AND hc.relname = 'request_logs_hot'
			   AND pn.nspname = 'public'
			   AND pc.relname = 'request_logs'
			   AND h.attnum > 0 AND NOT h.attisdropped
			   AND p.attnum > 0 AND NOT p.attisdropped
			   AND h.attname NOT IN ('customer_id', 'request_class', 'due_at')
		`).Scan(&baseCols); err != nil {
			return fmt.Errorf("derive request_logs base view columns: %w", err)
		}
		if baseCols == "" {
			return fmt.Errorf("request_logs view self-heal: empty base column list " +
				"(request_logs_hot/request_logs missing? run migration 341 first)")
		}
		if _, err := d.pool.Exec(ctx, fmt.Sprintf(`
			CREATE VIEW public.request_logs_with_current_month_without_customer_id AS
			SELECT %s FROM public.request_logs_hot
			UNION ALL
			SELECT %s FROM public.request_logs
		`, baseCols, baseCols)); err != nil {
			return fmt.Errorf("rebuild request_logs base wrapper view: %w", err)
		}
	}

	if !classWrapperExists {
		// Migration 577 阶段：lateral 追加 customer_id（hot 优先）。
		if _, err := d.pool.Exec(ctx, `
			CREATE VIEW public.request_logs_with_current_month_without_request_class_due_at AS
			SELECT v.*, m.customer_id
			FROM public.request_logs_with_current_month_without_customer_id v
			LEFT JOIN LATERAL (
				SELECT customer_id FROM public.request_logs_hot
				WHERE request_id = v.request_id AND ts = v.ts
				UNION ALL
				SELECT customer_id FROM public.request_logs
				WHERE request_id = v.request_id AND ts = v.ts
				LIMIT 1
			) m ON true
		`); err != nil {
			return fmt.Errorf("rebuild request_logs customer_id wrapper view: %w", err)
		}
	}

	// Migration 610 阶段：lateral 追加 request_class/due_at（canonical）。
	if _, err := d.pool.Exec(ctx, `
		CREATE VIEW public.request_logs_with_current_month AS
		SELECT v.*, source.request_class, source.due_at
		FROM public.request_logs_with_current_month_without_request_class_due_at v
		LEFT JOIN LATERAL (
			SELECT h.request_class, h.due_at
			FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT p.request_class, p.due_at
			FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) source ON true
	`); err != nil {
		return fmt.Errorf("rebuild request_logs_with_current_month view: %w", err)
	}
	slog.Info("request_logs_with_current_month view chain restored (self-heal 680)")
	return nil
}
