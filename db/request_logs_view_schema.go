package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/internaltraffic"
)

// ensureRequestLogsCurrentMonthView mirrors
// sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql and
// sql/migrations/startup/710_request_logs_view_session_family_v2.sql.
//
// 2026-09-07 事故自愈：共享 PG 上的 canonical 查询视图
// request_logs_with_current_month 被带外操作删掉（577/610 的包装视图
// …_without_customer_id、…_without_request_class_due_at 仍在，顶层缺失），
// /api/logs 全部请求 42P01。写路径（telemetry → request_logs_hot）不受影响，
// 只有读路径挂——这类"表在、视图没了"的状态没有任何 migration 会重建
// （610 之后再无 migration 触碰该视图），网关也没有启动自愈，于是持续报错。
//
// 2026-09-14 存储优化方案 v2 S2（plan §3 D6 / §4）：canonical 升级为
// session 家族拼装体——session_turns(_hot) 113 列会话投影 UNION ALL v1 体
// （700 形态冻结）× 反连接（request_id 已入 turns 的 v1 行不再输出，双写期
// 不重复计数）。约 40 个读方同名同列契约零改动切换（migration 710 与本函数
// 双轨同体；db/view_schema_v2_contract_test.go 校验两者 viewdef 等价）。
// session_turns 缺表的极简/陈旧库回退 v1 体（legacy 链保持既有形状探测）。
//
// 重建按 577 + 610 + 696 + 700 的包装链分阶段补齐，列契约 = 基础 108 列 UNION
//   - customer_id (577) + request_class/due_at (610) + system_fingerprint (696)
//   - raw_model_name (700，仅冻结链需追加；动态推导的基础交集自带)
//   - credits_rate_multiplier (738/R57 六点闭合：基座缺列时条件 lateral，
//     否则自愈重建体缺列 → bg rollup 每分钟 column does not exist，738 事故
//     经自愈通道复发形态) + client_ip (740/R57 B7：真源客户端 IP 进链，
//     bg rollup client_ip 维度与看板 client_ips 饼图的数据前提)。
//
// 基础列取 hot∩parent 交集（排除后追加列），因此 hot 的 HOT_ONLY 列
// （caller_id 等）永远不会撑爆 UNION——这正是 341 式 "SELECT * FROM hot
// UNION ALL SELECT * FROM parent" 重放在 603 之后必失败、进而留下"视图已删
// 未建"的根因。700 的缘起：drift scanner（83bf582dd 起）SELECT
// raw_model_name FROM 本视图，而冻结基础交集早于 485，该列从未出现在链上，
// 每周期 42703（本机 2089 部署即时炸出，生产 252 视图同构）。
//
// 编号 SQL 文件供 DBA 同步流程与 deploy-local 修复轨道使用；本函数保证
// 二进制启动即生效（幂等：视图健康且为 v2 体时零 DDL）。
func (d *DB) ensureRequestLogsCurrentMonthView(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	// 734：details 特征层在场时，最新 v3 体须含 session_turn_details JOIN；
	// 老库（733 未跑）回退 710 形态仍视为健康，零 DDL。
	var detailsFamilyExists bool
	if err := d.pool.QueryRow(ctx, `
		SELECT to_regclass('public.session_turn_details_hot') IS NOT NULL
		   AND to_regclass('public.session_turn_details') IS NOT NULL
	`).Scan(&detailsFamilyExists); err != nil {
		return fmt.Errorf("probe session_turn_details family: %w", err)
	}
	var canonicalExists, bodyIsV2 bool
	// 体形探测基于 pg_views 行内求值（视图缺失时 EXISTS 子查询零行，
	// pg_get_viewdef 不会被求值——不能直接对 '...'::regclass 取视图定义，
	// 视图缺失会 42P01 炸掉整个探针）。
	if err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
		),
		EXISTS (
			SELECT 1 FROM pg_views
			WHERE schemaname = 'public'
			  AND viewname = 'request_logs_with_current_month'
			  AND pg_get_viewdef(schemaname || '.' || viewname, true) LIKE '%session_turns%'
			  AND (NOT $1 OR pg_get_viewdef(schemaname || '.' || viewname, true) LIKE '%session_turn_details%')
		)
	`, detailsFamilyExists).Scan(&canonicalExists, &bodyIsV2); err != nil {
		return fmt.Errorf("probe request_logs_with_current_month existence: %w", err)
	}
	if canonicalExists && bodyIsV2 {
		return nil
	}
	if !canonicalExists {
		slog.Warn("request_logs_with_current_month missing; self-healing view chain " +
			"(mirror of sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql)")
	}

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

	// 会话族在场（430+707）→ canonical 直接上 v2 拼装体。v2 体内部内联的
	// v1 分支按 700 形态经 lateral 自带 customer_id/request_class/due_at/
	// system_fingerprint/raw_model_name，对动态重建链（基础交集已含 fp/raw）
	// 与冻结链（696/700 lateral 追加）同构——无需再探测包装形状。
	var sessionTurnsExist bool
	if err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'session_turns'
		)
	`).Scan(&sessionTurnsExist); err != nil {
		return fmt.Errorf("probe session_turns presence: %w", err)
	}
	if sessionTurnsExist {
		// 列数契约守卫（与 710 同规）：v1 体制 113 列（pre-738）或 115 列
		// （738+740 追加倍率/client_ip 后的 v1 体）；其余即冻结基础交集契约
		// 漂移，强行 UNION 必失败——保留现状，由视图契约修复流程先归位。
		if canonicalExists {
			var canonCols int
			if err := d.pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'request_logs_with_current_month'
		`).Scan(&canonCols); err != nil {
				return fmt.Errorf("probe request_logs_with_current_month column count: %w", err)
			}
			// 813 前的形态：113 列（710 体）/ 115 列（738+740 追加倍率/client_ip
			// 后的 v1 体）。118 列（813 三列）只会出现在**会话体**上，而会话体
			// 早在上面的 bodyIsV2 分支就返回了 —— 走到这里说明视图还是 v1 体，
			// 所以集合不必含 118。其余即冻结基础交集契约漂移，强行 UNION 必
			// 失败——保留现状，由视图契约修复流程先归位。
			if canonCols != 113 && canonCols != 115 {
				slog.Warn("request_logs_with_current_month column count not in {113,115} (frozen contract drift); keeping v1 body",
					"columns", canonCols)
				return nil
			}
			slog.Info("request_logs_with_current_month carries the v1 body; upgrading to the " +
				"session-family v2 body (mirror of sql/migrations/startup/710_request_logs_view_session_family_v2.sql)")
		}
		baseHasFP, baseHasRaw, baseHasCredits, baseHasCIP, err := d.baseWrapperShape(ctx)
		if err != nil {
			return err
		}
		middleCols, err := d.middleWrapperCols(ctx)
		if err != nil {
			return err
		}
		if _, err := d.pool.Exec(ctx, canonicalV2DDL(middleCols, baseHasFP, baseHasRaw, baseHasCredits, baseHasCIP, detailsFamilyExists)); err != nil {
			return fmt.Errorf("rebuild request_logs_with_current_month as session-family v2: %w", err)
		}
		if _, err := d.pool.Exec(ctx, `COMMENT ON VIEW public.request_logs_with_current_month IS `+
			canonicalV2Comment); err != nil {
			return fmt.Errorf("comment request_logs_with_current_month v2: %w", err)
		}
		slog.Info("request_logs_with_current_month view upgraded to session-family v2 body "+
			"(session_turns(_hot) projection UNION ALL frozen v1 body with anti-join dedup)",
			"details_join", detailsFamilyExists)
		return nil
	}

	// 无会话族（极简/陈旧库）：维持 v1 体。610+696+700 阶段：lateral 追加
	// request_class/due_at（canonical），并按基础包装形状与底层表实际列况
	// 决定是否追加 system_fingerprint（696）与 raw_model_name（700，与
	// sql/migrations/startup/700 同形）。post-603 动态推导的基础交集已含两列
	// （680 引导/自愈重建路径），此时 canonical 经 v.* 继承，再 lateral 追加
	// 会重复列（CREATE 直接失败）；pre-485 冻结链（生产/本机现网）基础包装
	// 缺列，需 lateral 追加。lateral 的列引用同样按 hot 侧实况裁剪——
	// hot/parent 本身缺列时（极简测试桩、落后于 485/603 的库）自愈不得比
	// 缺列现状更坏。列名消费者不受形状间列序差异影响。
	if canonicalExists {
		// v1 体已在且会话族缺席：无需任何变更（保持既有形状，零 DDL）。
		return nil
	}
	baseHasFingerprint, baseHasRawModelName, baseHasCredits, baseHasClientIP, err := d.baseWrapperShape(ctx)
	if err != nil {
		return err
	}
	var hotHasFingerprint, hotHasRawModelName, hotHasCredits, hotHasClientIP bool
	if err := d.pool.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_hot'
				  AND column_name = 'system_fingerprint'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_hot'
				  AND column_name = 'raw_model_name'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_hot'
				  AND column_name = 'credits_rate_multiplier'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_hot'
				  AND column_name = 'client_ip'
			)
	`).Scan(&hotHasFingerprint, &hotHasRawModelName, &hotHasCredits, &hotHasClientIP); err != nil {
		return fmt.Errorf("probe request_logs_hot appended columns: %w", err)
	}
	// 追加序与 canonicalV2DDL 逐字一致：class, due, fp, raw, credits, client_ip
	// （时间序 = 追加迁移序；两侧文本漂移会破坏 ensure↔迁移 viewdef 等价契约）。
	appendSelects := []string{"source.request_class", "source.due_at"}
	lateralHotSelects := []string{"h.request_class", "h.due_at"}
	lateralParentSelects := []string{"p.request_class", "p.due_at"}
	if !baseHasFingerprint && hotHasFingerprint {
		appendSelects = append(appendSelects, "source.system_fingerprint")
		lateralHotSelects = append(lateralHotSelects, "h.system_fingerprint")
		lateralParentSelects = append(lateralParentSelects, "p.system_fingerprint")
	}
	if !baseHasRawModelName && hotHasRawModelName {
		appendSelects = append(appendSelects, "source.raw_model_name")
		lateralHotSelects = append(lateralHotSelects, "h.raw_model_name")
		lateralParentSelects = append(lateralParentSelects, "p.raw_model_name")
	}
	if !baseHasCredits && hotHasCredits {
		appendSelects = append(appendSelects, "source.credits_rate_multiplier")
		lateralHotSelects = append(lateralHotSelects, "h.credits_rate_multiplier")
		lateralParentSelects = append(lateralParentSelects, "p.credits_rate_multiplier")
	}
	if !baseHasClientIP && hotHasClientIP {
		appendSelects = append(appendSelects, "source.client_ip")
		lateralHotSelects = append(lateralHotSelects, "h.client_ip")
		lateralParentSelects = append(lateralParentSelects, "p.client_ip")
	}
	canonicalDDL := fmt.Sprintf(`
		CREATE VIEW public.request_logs_with_current_month AS
		SELECT v.*, %s
		FROM public.request_logs_with_current_month_without_request_class_due_at v
		LEFT JOIN LATERAL (
			SELECT %s
			FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT %s
			FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) source ON true
	`, strings.Join(appendSelects, ", "),
		strings.Join(lateralHotSelects, ", "),
		strings.Join(lateralParentSelects, ", "))
	if _, err := d.pool.Exec(ctx, canonicalDDL); err != nil {
		return fmt.Errorf("rebuild request_logs_with_current_month view: %w", err)
	}
	slog.Info("request_logs_with_current_month view chain restored (self-heal 680+696+700)",
		"base_wrapper_carries_fingerprint", baseHasFingerprint,
		"base_wrapper_carries_raw_model_name", baseHasRawModelName,
		"appended_columns", strings.Join(appendSelects, ", "))
	return nil
}

// baseWrapperShape probes whether the base intersection wrapper already
// carries the post-freeze appended columns — system_fingerprint/raw_model_name
// (dynamic-rebuild chain) versus not (frozen pre-485 chain) — plus
// credits_rate_multiplier (736/738) and client_ip (341/740). Missing base
// wrapper (nothing to probe) reports all false — the lateral-append shape,
// matching the ensure's rebuild order.
func (d *DB) baseWrapperShape(ctx context.Context) (fp, raw, credits, cip bool, err error) {
	err = d.pool.QueryRow(ctx, `
		SELECT
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_with_current_month_without_customer_id'
				  AND column_name = 'system_fingerprint'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_with_current_month_without_customer_id'
				  AND column_name = 'raw_model_name'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_with_current_month_without_customer_id'
				  AND column_name = 'credits_rate_multiplier'
			),
			EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = 'request_logs_with_current_month_without_customer_id'
				  AND column_name = 'client_ip'
			)
	`).Scan(&fp, &raw, &credits, &cip)
	if err != nil {
		return false, false, false, false, fmt.Errorf("probe base wrapper appended columns: %w", err)
	}
	return fp, raw, credits, cip, nil
}

// sessionFamilyProjectionV2 是 710 会话分支的 113 列投影（别名固定 t；hot 与
// parent 两分支复用同一文本）。列映射契约（直映/派生/守卫 CAST/NULL 补位）
// 逐列登记见 sql/migrations/startup/710_request_logs_view_session_family_v2.sql
// 头注释；修改任一侧必须同步另一侧（contract 测试校验 viewdef 等价）。
// projectionExprsV2 holds the 113 session-branch expressions in canonical
// column order. init-time composition joins them with
// canonicalColumnOrderV2 into `+"`"+`expr AS name`+"`"+` pairs — bare NULL casts would
// otherwise name view columns after their type ("int8"/"text"), colliding
// into 42701 duplicate column names.
var projectionExprsV2 = []string{
	"NULL::bigint",
	"t.request_id",
	"t.ts",
	"t.tenant_id::text",
	"(CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END)",
	"(CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END)",
	"t.end_user_id",
	"NULL::text",
	"t.model",
	"(CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint END)",
	"NULL::bigint",
	"t.canonical_id",
	"NULL::text",
	"t.submit_mode",
	"t.prompt_tokens",
	"t.completion_tokens",
	"NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0)",
	"t.cost_usd::numeric(14,8)",
	"t.latency_ms",
	"t.success",
	"t.error_kind",
	"t.search_text",
	"t.cache_read_tokens",
	"t.cache_write_tokens",
	"t.identity_hash",
	"t.virtual_client_id",
	"NULL::text",
	"NULL::text",
	"NULL::boolean",
	"t.stream_first_chunk_ms",
	"t.stream_chunk_count",
	"t.stream_interrupted",
	"t.stream_done_sent",
	"t.request_checksum",
	"t.response_checksum",
	"NULL::text",
	"t.egress_protocol",
	"t.failure_stage",
	"t.failure_detail_code",
	"t.request_preview",
	"t.transform_summary",
	"t.response_preview",
	"t.stream_done_sent",
	"t.cost_display::numeric(14,8)",
	"t.cost_currency",
	"t.usage_source",
	"(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)",
	"NULL::text",
	sessionRequestStatusExpr,
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"NULL::text",
	"t.is_auto_request",
	"t.task_type",
	"NULL::text",
	"(CASE WHEN NULLIF(t.auto_decision, '') IS NOT NULL THEN t.auto_decision::jsonb END)",
	"t.auto_confidence::numeric(4,3)",
	"t.work_type",
	"t.task_type_chosen",
	"NULL::numeric(4,3)",
	"NULL::text",
	"NULL::text",
	"t.credits_charged",
	"t.parent_request_id",
	"NULL::text",
	"t.compression_strategy",
	"t.compression_meta",
	"NULL::integer",
	"NULL::integer",
	"NULL::jsonb",
	"NULL::text[]",
	"NULL::jsonb",
	"NULL::numeric(3,2)",
	"t.upstream_finish_reason",
	"t.tools",
	"t.client_endpoint",
	"t.client_timeout",
	"NULL::integer",
	"NULL::integer",
	"t.client_request_id",
	"t.upstream_status_code",
	"NULL::text[]",
	"NULL::text",
	"NULL::text",
	"NULL::jsonb",
	"(CASE WHEN t.attachment_count IS NULL THEN NULL WHEN t.attachment_count > 0 THEN true ELSE false END)",
	"t.attachment_count",
	"t.routing_attempts",
	"t.routing_summary",
	"t.agent_name",
	"t.agent_type",
	"t.client_protocol::character varying(50)",
	"t.canonical_model",
	"t.t0_arrived_at",
	"t.t1_total_enqueued_at",
	"t.t2_total_dequeued_at",
	"t.t3_model_enqueued_at",
	"t.t4_model_dequeued_at",
	"t.t5_cred_enqueued_at",
	"t.t6_cred_dequeued_at",
	"t.t7_forward_start_at",
	"t.t8_response_start_at",
	"t.t9_response_end_at",
	"NULL::text",
	"t.is_final_success",
	"t.origin_actor::character varying(255)",
	"t.customer_id",
	"NULL::text",
	"NULL::timestamptz",
	"t.system_fingerprint",
	"t.raw_model_name",
	// 738 追加尾列：session 分支无源补位（NULL::double precision）。
	"NULL::double precision",
	// 740 追加的 client_ip：**816 起改为 session 侧有源投影**（审计 §9.60.6.1）。
	//
	// 原注释写「session_turns.client_ip 为 text 且未回填，不能直映」——两半都要改：
	// 「未回填」是**错的**（本机 7 天 172,305/202,774 = 85.0% 有值；252 7 天 17,586 行
	// 有值），而「text 不能直映」只说明它**需要一次显式转换**，不是不能映。
	//
	// 与 v1 同义的依据（252 生产库，审计 §9.60.6.1）：同 request_id 配对 826 行
	// session_turns.client_ip == host(request_logs.client_ip) **826/826、差异 0**。
	// 本机那条「两列 100% 相同 ⇒ 它是转发头副本」的旧裁决已被证伪——本机全库
	// client_forwarded_for 只有 6 个 distinct 取值、多跳链路 0 条，零分辨力。
	//
	// 为什么带守卫而不是直转：session_turns.client_ip 是 **text**，没有类型
	// 约束，一个畸形值会让 `::inet` 抛错并**打挂整条 canonical 视图的每一个读方**。
	// 代价是「畸形 → NULL」而不是「整条查询报错」。
	//
	// **守卫判据在 817 换成 pg_input_is_valid**（审计 §9.64）。816 用的是字符类
	// 正则 `^[0-9a-fA-F:.]+$`，它**只挡住非字符集的垃圾**：
	// `192.168.1` / `deadbeef` / `1.2.3.4.5.6` / `:::` / `...` / `999.1.1.1`
	// **全部通过那个正则**，然后在 `::inet` 上抛错——真库实测已复现。
	// 我在 §9.61 报过一张守卫行为表（garbage / 多跳链 / 空串 全落 NULL），
	// **那张表只测了字符类不合法的值，于是给了「守卫已覆盖」一个过宽的结论**。
	// `pg_input_is_valid(v,'inet')`（PG 16+，本仓一致跑 PG 17）是**完整的语义
	// 判据**：同一批坏值 192.168.1→f、deadbeef→f、':::'→f，而 '1.2.3.4' 与
	// '2a06:98c0:3600::103'→t。
	//
	// 写方侧也补了一道 net.ParseIP（并行会话，origin_mw.resolveClientIP）——
	// 但那**只保护新写入的行**：库里已有的值、以及任何别的写入方仍会进到这里，
	// 所以读侧的语义判据不能省。
	"(CASE WHEN pg_input_is_valid(t.client_ip, 'inet') THEN t.client_ip::inet END)",
	// 813 追加尾列（§9.22）：三列的 session 侧源在 session_turns 上，且与 v1
	// 逐值一致（真库 1,515,960 行同 request_id 配对实测 both_differ=0），
	// 缺的只是「近窗覆盖率」——近 7 天 origin_stage/client_forwarded_for 已达
	// 86%。trace_events 同批**不**投影：镜像从未写它（1d/7d/30d 非空率恒为
	// 0），而 v1 侧有 691,883 行带值且已被反连接丢弃 ⇒ 投影它等于把数据
	// 换成 NULL，是 §9.18「修好了但变全盲」的同一形状。
	"t.origin_stage",
	"t.token_band",
	"t.client_forwarded_for",
}

// sessionFamilyProjectionV2 is the session branch projection for BOTH turn
// sources (alias fixed to t), each expression explicitly named to the frozen
// contract. Column mapping contract: sql/migrations/startup/
// 710_request_logs_view_session_family_v2.sql header.
// detailsProjectionColumns is the 734 view-contract group: details column
// name → the v1 frozen-contract NULL type. withDetails=true projections emit
// `d.<name>` at these positions; false falls back to the 710 NULL placeholders.
// Types match public.session_turn_details DDL (migration 733) exactly, so the
// UNION ALL positional typing is unchanged in both shapes.
var detailsProjectionColumns = map[string]string{
	"client_model": "text", "provider_id": "bigint", "client_profile": "text",
	"virtual_ip": "text", "virtual_mac": "text", "affinity_hit": "boolean",
	"transform_rule_id": "text", "gw_task_id": "text", "api_key_prefix": "text",
	"owner_user": "text", "application_code": "text", "key_alias": "text",
	"api_key_owner_user": "text", "auto_profile": "text", "confidence_num": "numeric(4,3)",
	"model_chosen": "text", "strategy_used": "text", "compression_reason": "text",
	"outbound_msg_count": "integer", "outbound_token_est": "integer", "outbound_msg_hashes": "jsonb",
	"quality_flags": "text[]", "quality_fix_actions": "jsonb", "quality_score": "numeric(3,2)",
	"stream_chunk_errors": "integer", "stream_chunks_sent": "integer", "attachments": "jsonb",
	"request_type": "text", "request_class": "text", "due_at": "timestamptz",
}

// buildSessionProjectionExprs composes the session-branch expressions for an
// explicit canonical column-name list: details positions flip between NULL
// placeholders (710 shape) and d.<col> (734 shape); everything else comes
// from projectionExprsV2 **looked up by column name**.
//
// 按名查而不是按下标取，是 815 引入 preRateColumnOrder 之后的硬要求：回退
// 列序不是完整列序的前缀（credits/client_ip 插在 815 三列之前），按下标取
// 会把 `NULL::double precision` 贴到 `origin_stage` 这个名字上——列名对得上
// 而列是错的，UNION ALL 只按位置匹型，所以它不报错、只是让 pre-736 回退体
// 的三列恒 NULL。守卫 TestViewV2ProjectionContractSync 的三形态断言就是
// 为这条而设：它读的是 `t.<col> AS <col>` 这段文本，下标错位会立刻变红。
func buildSessionProjectionExprs(names []string, withDetails bool) []string {
	out := make([]string, len(names))
	for i, name := range names {
		if withDetails {
			if _, ok := detailsProjectionColumns[name]; ok {
				out[i] = "d." + name
				continue
			}
		}
		expr, ok := projectionExprByColumn[name]
		if !ok {
			// 契约漂移：投影表里没有这一列。panic 而非静默产出空表达式——
			// 静默会渲染出 ` AS origin_stage` 这种语法错，错误现场离真因很远。
			panic(fmt.Sprintf("session projection: no expression registered for column %q", name))
		}
		out[i] = expr
	}
	return out
}

// projectionExprByColumn 是 projectionExprsV2 的按名索引，与
// canonicalColumnOrderV2 一一对应（init 期自校验长度与逐位一致）。
var projectionExprByColumn = func() map[string]string {
	m := make(map[string]string, len(projectionExprsV2))
	for i, name := range canonicalColumnOrderV2 {
		if i >= len(projectionExprsV2) {
			panic(fmt.Sprintf("projectionExprsV2 shorter than canonicalColumnOrderV2 at %q", name))
		}
		m[name] = projectionExprsV2[i]
	}
	if len(projectionExprsV2) != len(canonicalColumnOrderV2) {
		panic(fmt.Sprintf("projectionExprsV2 has %d entries, canonicalColumnOrderV2 has %d",
			len(projectionExprsV2), len(canonicalColumnOrderV2)))
	}
	return m
}()

// middleWrapperCols returns the middle wrapper's explicit column list
// (quote_ident'd, attnum order) minus the 738/740 appended columns — the
// inner-select base of the canonical v2 composition. Both the ensure and
// migration 740 derive this list with the same query so the composed viewdefs
// render identically.
func (d *DB) middleWrapperCols(ctx context.Context) (string, error) {
	var cols string
	if err := d.pool.QueryRow(ctx, `
		SELECT COALESCE(string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum), '')
		  FROM pg_attribute a
		 WHERE a.attrelid = 'public.request_logs_with_current_month_without_request_class_due_at'::regclass
		   AND a.attnum > 0 AND NOT a.attisdropped
		   AND a.attname NOT IN ('credits_rate_multiplier', 'client_ip')
	`).Scan(&cols); err != nil {
		return "", fmt.Errorf("derive middle wrapper column list: %w", err)
	}
	if cols == "" {
		return "", fmt.Errorf("request_logs view self-heal: empty middle wrapper column list")
	}
	return cols, nil
}

// sessionRequestStatusExpr is the session-leg expression for the frozen v1
// `request_status` column. It is a package-level const (not a function) so that
// the view-ensure chain, both native readers, and migration 824 all render the
// identical text — the viewdef-equivalence contract in
// db/view_schema_v2_contract_test.go compares the composed DDL verbatim.
//
// ── Why the error_kind branch exists (audit §9.160) ────────────────────────
//
// The previous expression classified a turn as `rate_limited` only via
// `t.status_code = 429`. Measured on the local real database
// (1,688,629 rows in session_turns, 2026-09/10):
//
//   - `status_code = 429` matches **0** rows. Not one, in the whole table.
//   - 437,402 rows are genuine rate-limited turns, and every one of them
//     carries `status_code = 500` on the session side.
//   - So the `rate_limited` arm was **dead code on the session leg**: it could
//     never fire. All 437,402 turns were reported as plain `failure`.
//   - The only signal that survives the mirror is `error_kind`:
//     `error_kind = 'rate_limit_exceeded'` ⟺ v1 `request_status =
//     'rate_limited'` with **0 counterexamples in either direction**
//     (394,614 twin rows) and 0 rows with `success = true`.
//
// Effect of the old expression on session_turns:
//
//	failure 1,358,245 / success 330,384 / rate_limited 0
//	after:   failure   920,843 / success 330,384 / rate_limited 437,402
//
// i.e. the canonical view — and the eight native readers that share this
// projection — overstated `failure` by 32% and under-reported `rate_limited`
// as 0. This is not a retirement-only concern: it is wrong **today**, and once
// request_logs is dropped the view would report zero rate-limited requests
// system-wide.
//
// ── Why the status_code = 429 arm is kept ──────────────────────────────────
//
// It is dead on today's data but it is the shape a future upstream 429 would
// take, and dropping it would make the expression silently narrower than the
// one it replaces. Keeping it costs nothing and can only add correct rows.
//
// ── Why the NULL-success guard stays first ────────────────────────────────
//
// Unchanged contract, deliberately: `success IS NULL` still yields NULL rather
// than a guessed label. Local data has 0 such rows, so this is latent, and
// preserving it keeps the change provably scoped to the rate_limited arm.
// (The admin session-detail reader had the matching latent hole — it scanned
// `success` into a bare bool and dropped the whole row on NULL; fixed there.)
//
// ── Why the IN-set, not just 'rate_limit_exceeded' (R41 audit F1) ──────────
//
// The historical cross-tab above proved the single-value form only on
// pre-823 data, where mirrors never recorded request_status at all. The
// write side emits RequestStatusRateLimited with exactly two error_kind
// literals: 'rate_limit_exceeded' (gateway RPM/concurrent rejections,
// handler.go:3244 / messages.go:433 / responses.go) and 'key_throttled'
// (handler.go:2376, api-key anomalous-usage throttle). Post-823 mirrors
// carry the rate_limited label while keeping error_kind='key_throttled';
// a single-value arm would file every such row under 'failure' forever —
// and the view's session leg wins over the v1 leg, so nothing downstream
// could correct it.
const sessionRequestStatusExpr = `(CASE ` +
	`WHEN t.success IS NULL THEN NULL ` +
	`WHEN t.success THEN 'success' ` +
	`WHEN t.error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited' ` +
	`WHEN t.status_code = 429 THEN 'rate_limited' ` +
	`ELSE 'failure' END)`

// sessionFamilyProjectionFor renders the named projection (details-aware) for
// an explicit canonical name list.
func sessionFamilyProjectionFor(names []string, withDetails bool) string {
	exprs := buildSessionProjectionExprs(names, withDetails)
	if len(exprs) != len(names) {
		panic(fmt.Sprintf("session projection drift: %d exprs vs %d contract names",
			len(exprs), len(names)))
	}
	parts := make([]string, len(exprs))
	for i, expr := range exprs {
		parts[i] = expr + " AS " + names[i]
	}
	return strings.Join(parts, ",\n\t\t")
}

// sessionFamilyProjection renders the full canonical-column projection
// (details-aware). Both the view-ensure chain and native readers go through
// this one composer.
func sessionFamilyProjection(withDetails bool) string {
	return sessionFamilyProjectionFor(canonicalColumnOrderV2, withDetails)
}

// canonicalV2Comment 与迁移 734 的 COMMENT ON VIEW 同文（COMMENT 的 IS 只收
// 单个字面量，不能像 SQL 赋值那样 || 拼接）。824 起补记 request_status 的分类
// 口径（816/817 的两次 client_ip 改写当时未同步本常量，视图注释一度落后两代；
// 这里补的是最新状态，不是逐版流水）。
const canonicalV2Comment = `'会话存储解耦 v3（824）: 710 拼装体 + 734 details 特征层 LEFT JOIN（733，键 tenant_id+request_id+partition_date）——30 个 NULL 占位列换真实特征列（client_model/quality_*/stream_chunk_errors/request_class 等）。813 再追加 3 列（origin_stage/token_band/client_forwarded_for，session 侧直映 t.<col>、v1 侧 lateral）。816/817 把 client_ip 从 NULL 补位改为有源投影并换成语义守卫。824 把 request_status 的会话腿改走 error_kind=''rate_limit_exceeded''（旧口径只看 status_code=429，而 session_turns 里 429 是 0 行，等于从未生效，审计 §9.160）。仍 NULL：id（v1 请求行 id ≠ session turn id）/test_col/test_tab_indent/provider_model/credits_rate_multiplier，以及 client_ip 本身（字面量非法时落 NULL，不抛错）。trace_events 刻意未投影（镜像从不写，近窗非空率 0，投影即净数据损失）。'`

// canonicalColumnOrderV2 是 115 列的契约顺序（= 现网 canonical 视图列序：
// 710 冻结 113 列 + 738 credits_rate_multiplier + 740 client_ip）。
// UNION ALL 按位置匹型：会话分支按此顺序展开投影；v1 分支内层（包装链 +
// lateral）在冻结/动态两形态下列序不同（fp/raw 位置漂移），故外面包一层
// 按名归一化的显式投影——两形态下内层都恰好携带全部 113 个名字。
var canonicalColumnOrderV2 = []string{
	"id", "request_id", "ts", "tenant_id", "application_id", "api_key_id",
	"end_user_id", "client_model", "outbound_model", "credential_id",
	"provider_id", "canonical_id", "client_profile", "request_mode",
	"prompt_tokens", "completion_tokens", "total_tokens", "cost_usd",
	"latency_ms", "success", "error_kind", "search_text", "cache_read_tokens",
	"cache_write_tokens", "identity_hash", "virtual_client_id", "virtual_ip",
	"virtual_mac", "affinity_hit", "stream_first_chunk_ms", "stream_chunk_count",
	"stream_interrupted", "stream_done_sent", "request_checksum",
	"response_checksum", "transform_rule_id", "egress_protocol",
	"failure_stage", "failure_detail_code", "request_preview",
	"transform_summary", "response_preview", "stream_done_received",
	"cost_display", "cost_currency", "usage_source", "gw_session_id",
	"gw_task_id", "request_status", "api_key_prefix", "owner_user",
	"application_code", "key_alias", "api_key_owner_user", "is_auto_request",
	"task_type", "auto_profile", "auto_decision", "auto_confidence",
	"work_type", "task_type_chosen", "confidence_num", "model_chosen",
	"strategy_used", "credits_charged", "parent_request_id",
	"compression_reason", "compression_strategy", "compression_meta",
	"outbound_msg_count", "outbound_token_est", "outbound_msg_hashes",
	"quality_flags", "quality_fix_actions", "quality_score",
	"upstream_finish_reason", "tool_calls", "client_endpoint", "client_timeout",
	"stream_chunk_errors", "stream_chunks_sent", "client_request_id",
	"upstream_status_code", "test_col", "test_tab_indent", "provider_model",
	"attachments", "has_attachments", "attachment_count", "routing_attempts",
	"routing_summary", "agent_name", "agent_type", "client_protocol",
	"canonical_model", "t0_arrived_at", "t1_total_enqueued_at",
	"t2_total_dequeued_at", "t3_model_enqueued_at", "t4_model_dequeued_at",
	"t5_cred_enqueued_at", "t6_cred_dequeued_at", "t7_forward_start_at",
	"t8_response_start_at",
	"t9_response_end_at",
	"request_type",
	"is_final_success",
	"origin_actor",
	"customer_id",
	"request_class",
	"due_at",
	"system_fingerprint",
	"raw_model_name",
	// 738（R56）+ 740（R57 B7）追加尾列：自愈重建体必须携带，否则 bg
	// rollup（r.credits_rate_multiplier）与 client_ip 维度在读端缺列。
	"credits_rate_multiplier",
	"client_ip",
	// 813 追加尾列：origin_stage/token_band/client_forwarded_for。id 刻意
	// 不在其中——真库 1,515,984 组同 request_id 配对里 r.id = t.id 命中
	// **0** 次：v1 的 request_logs.id 是请求行 id，session_turns.id 是 turn
	// id，判据是「同一个东西」而不是「session 侧有这个名字」。
	"origin_stage",
	"token_band",
	"client_forwarded_for",
}

// preRateColumnOrder 是 pre-736/740 极简链回退用的列序：113 冻结契约 + 813
// 的三列，**剔除** credits_rate_multiplier/client_ip（那两列的源在中层包装
// 自身，极简链上没有）。
//
// 刻意不复用 canonicalColumnOrderV2 的前缀切片：追加列不是接在 113 之后的
// ——credits/client_ip 插在 813 三列之前，前缀切片会把它们带进来，而
// canonicalV2DDL 的 pre-736 回退分支恰恰要求它们缺席。
func preRateColumnOrder() []string {
	out := make([]string, 0, len(canonicalColumnOrderV2))
	for _, n := range canonicalColumnOrderV2 {
		if n == "credits_rate_multiplier" || n == "client_ip" {
			continue
		}
		out = append(out, n)
	}
	return out
}

// canonicalV2DDL 组装 710/734 视图体 + 738/740 追加尾列。middleCols 是中层
// 包装（request_logs_with_current_month_without_request_class_due_at）的显式
// 列清单——已剔除 credits_rate_multiplier/client_ip；两者以 v.<col> 文本引用
// 固定在内层末尾（= 738 regexp 的插入位）。刻意不用 v.*：星展开的内容随中
// 层扩列时变（710/734 存储的展开冻结于其 CREATE 时刻，自愈组合则取当下中
// 层），任何依赖星展开的形态都无法与迁移产物逐字节对齐，也无法被 740.down
// 稳定还原成 738 形态。
//
// baseHasFP/baseHasRaw 描述基础包装是否已自带 system_fingerprint/raw_model_name
// ——冻结链（577/610 时代交集）不带，v1 分支按 700 形态 lateral 追加；动态
// 重建链（680 引导/自愈，post-603 交集）自带，追加会重复列。这两列走
// lateral（734 原语义，源是 hot/parent 底表）；class/due 恒走 lateral。
// lateral 追加序固定 class, due, fp, raw。
//
// baseHasCredits/baseHasCIP 描述 736/738 倍率列与 341/740 client_ip 是否已
// 在中层包装上：在（738/740 迁移后的常态）→ 外层投影展开 118 名单、内层末
// 尾 v.<col> 引用；缺任一（<736 极简链）→ 回退 710 的 113 列契约 + 813 的
// 三列（preRateColumnOrder，剔除 credits/client_ip）。两者刻意不走
// lateral——源就是中层自身，lateral 化会改变引用文本打破等价契约。
//
// 外层按 canonicalColumnOrderV2 按名归一化后与会话分支按位置对齐；反连接
// 守卫走 idx_session_turns_request / idx_session_turns_hot_request。
//
// hasDetails（734）：true 时 session 分支 LEFT JOIN session_turn_details(_hot)
// 特征层，30 个 NULL 占位换 d.<col>；false（733 未跑的陈旧库）回退 710 形态。
//
// 组合文本与迁移 710/734/738/740 的产物逐字对齐——任何一侧漂移都会打破
// TestRequestLogsViewV2EnsureMatchesMigration 的 viewdef 等价契约。
func canonicalV2DDL(middleCols string, baseHasFP, baseHasRaw, baseHasCredits, baseHasCIP, hasDetails bool) string {
	names := canonicalColumnOrderV2
	if !baseHasCredits || !baseHasCIP {
		names = preRateColumnOrder() // pre-736/740 极简链：回退 710 冻结契约 + 813 三列
	}
	appendCols := []string{
		"source.request_class", "source.due_at",
		// 815（§9.22）：origin_stage/token_band/client_forwarded_for 在 session
		// 侧走 t.<col> 直映，v1 侧恒走 lateral——中层包装链（577/610/696/700
		// 形态）冻结于更早的列集，从不带这三列，所以不存在「基础链已自带」
		// 这一形态，不需要 fp/raw 那样的条件化。序固定在 fp/raw **之前**：
		// 与迁移 815 的 append_cols/lateral 字面量同序，viewdef 等价契约
		// 比的就是这段文本。
		"source.origin_stage", "source.token_band", "source.client_forwarded_for",
	}
	if !baseHasFP {
		appendCols = append(appendCols, "source.system_fingerprint")
	}
	if !baseHasRaw {
		appendCols = append(appendCols, "source.raw_model_name")
	}
	lateralCols := []string{
		"h.request_class", "h.due_at",
		"h.origin_stage", "h.token_band", "h.client_forwarded_for",
	}
	if !baseHasFP {
		lateralCols = append(lateralCols, "h.system_fingerprint")
	}
	if !baseHasRaw {
		lateralCols = append(lateralCols, "h.raw_model_name")
	}
	parentCols := make([]string, len(lateralCols))
	for i, c := range lateralCols {
		parentCols[i] = "p" + strings.TrimPrefix(c, "h")
	}
	// 738/740 追加尾列的内层引用（源 = 中层包装自身；序 = 迁移时间序）。
	vExtras := make([]string, 0, 2)
	if baseHasCredits {
		vExtras = append(vExtras, "v.credits_rate_multiplier")
	}
	if baseHasCIP {
		vExtras = append(vExtras, "v.client_ip")
	}
	innerSelects := append([]string{middleCols}, appendCols...)
	innerSelects = append(innerSelects, vExtras...)
	proj := sessionFamilyProjectionFor(names, hasDetails)
	hotJoin := ""
	parentJoin := ""
	if hasDetails {
		hotJoin = `
	LEFT JOIN public.session_turn_details_hot d
	  ON d.tenant_id = t.tenant_id
	 AND d.request_id = t.request_id
	 AND d.partition_date = t.partition_date`
		parentJoin = `
	LEFT JOIN public.session_turn_details d
	  ON d.tenant_id = t.tenant_id
	 AND d.request_id = t.request_id
	 AND d.partition_date = t.partition_date`
	}
	return fmt.Sprintf(`
	CREATE OR REPLACE VIEW public.request_logs_with_current_month AS
	SELECT %s
	FROM public.session_turns_hot t%s
	UNION ALL
	SELECT %s
	FROM public.session_turns t%s
	UNION ALL
	SELECT %s
	FROM (
		SELECT %s
		FROM public.request_logs_with_current_month_without_request_class_due_at v
		LEFT JOIN LATERAL (
			SELECT %s
			FROM public.request_logs_hot h
			WHERE h.request_id = v.request_id AND h.ts = v.ts
			UNION ALL
			SELECT %s
			FROM public.request_logs p
			WHERE p.request_id = v.request_id AND p.ts = v.ts
			LIMIT 1
		) source ON true
	) rl
	WHERE NOT EXISTS (SELECT 1 FROM public.session_turns_hot th WHERE th.request_id = rl.request_id)
	  AND NOT EXISTS (SELECT 1 FROM public.session_turns tp WHERE tp.request_id = rl.request_id)`,
		proj, hotJoin,
		proj, parentJoin,
		strings.Join(names, ", "),
		strings.Join(innerSelects, ", "),
		strings.Join(lateralCols, ", "),
		strings.Join(parentCols, ", "))
}

// SessionFamilyTurnsSourceSQL returns a parenthesized FROM-source emitting the
// frozen 113-column request-logs shape straight from the session-turn family
// (hot ∪ parent, details-joined per 734), for S3 wave-1 native readers
// (plan §4-S3: admin 日志读端分波去视图化). It reuses
// sessionFamilyProjection(true) so the view ensure chain and native readers
// share one copy of the column-mapping contract.
//
// Callers require migration 733 (session_turn_details family) to be applied —
// native readers run post-migration by construction (startup migrations
// precede serving).
//
// The returned source carries no alias — callers append one (e.g. `rl`).
// Unlike the 710 view it omits the frozen-v1 branch and anti-join entirely:
// rows promoted out of request_logs history are NOT covered, so native mode
// is only correct while request_logs still holds every not-yet-retired row
// (i.e. before S4 stop-write + TTL retirement of windows that predate the
// session mirror).
func SessionFamilyTurnsSourceSQL() string {
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date)"
}

// SessionFamilyTurnsForSessionSQL returns the same frozen-column source as
// SessionFamilyTurnsSourceSQL, but with the session predicate **pushed inside**
// both legs so it can use idx_session_turns_session (session_id, turn_no DESC).
//
// Why this exists (measured, not assumed): in SessionFamilyTurnsSourceSQL the
// projected `gw_session_id` is the expression
// `(CASE WHEN t.session_id LIKE 'sys:%' THEN NULL ELSE t.session_id END)`,
// so a caller filtering on the projected name cannot use that index. The
// view-based equivalent pays for the v1 branch's NOT EXISTS anti-join as well.
// Same session, same 21 rows, local EXPLAIN ANALYZE:
//
//	view  (request_logs_with_current_month): Execution 188.5ms, 10204 buffers
//	native (this helper):                     Execution   0.585ms, ~20 buffers
//
// That is why the S3 wave-1 helper must NOT be reused for session-scoped
// readers: it is tuned for ts-window log queries, not per-session lookups.
//
// Contract for callers:
//   - The session id MUST already be bound at $1; the predicate is emitted as
//     `t.session_id = $1` on both legs, so no outer gw_session_id filter is
//     needed (and adding one would re-introduce the expression filter).
//   - The projected `gw_session_id` keeps its NULL-for-`sys:` shape, so rows
//     are byte-identical to the view's session branch.
//   - Callers needing tenant scoping add their own `WHERE rl.tenant_id = $2`
//     or equivalent after the alias.
//
// The returned source carries no alias — callers append one (e.g. `rl`).
func SessionFamilyTurnsForSessionSQL() string {
	return "(SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns_hot t" +
		" LEFT JOIN public.session_turn_details_hot d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" WHERE t.session_id = $1" +
		" UNION ALL SELECT " + sessionFamilyProjection(true) +
		" FROM public.session_turns t" +
		" LEFT JOIN public.session_turn_details d" +
		" ON d.tenant_id = t.tenant_id AND d.request_id = t.request_id AND d.partition_date = t.partition_date" +
		" WHERE t.session_id = $1)"
}

// SessionFamilyBodiesSourceSQL returns a FROM source over the session-family
// bodies tables that exposes the **v1 column names** (`request_body` /
// `response_body`), so a caller can swap the relation name without touching
// its projection list.
//
// # 为什么必须有它（而不是「直接 JOIN session_bodies」）
//
// 两侧的**列名不同**（实测 information_schema，2026-10-05）：
//
//	v1   request_logs_bodies : request_id text NOT NULL, request_body jsonb, response_body jsonb
//	会话  session_bodies      : request_id text NOT NULL, request_delta jsonb, response_delta jsonb
//
// 语义相同（都是 JSONB），**名字不同**。所以「把 LEFT JOIN
// request_logs_bodies_with_current_month 换成 session_bodies」这种机械替换
// 会让调用点的 `COALESCE(rb.request_body, '{}'::jsonb)` 直接 42703。
// ⇒ 映射必须落在**这一层**，而不是散在每个调用点的 SELECT 里。
//
// 类型逐列核对过：两侧 bodies 列**都是 jsonb**，`request_id` **都是 text NOT NULL**
// ⇒ 纯改名，**不需要 cast**。（若将来一侧变成 text，这里必须加 `::jsonb`。
// 由 admin 侧的列名/类型对等门守着，见 admin/session_bodies_source_test.go。）
//
// # hot ∪ parent 两条腿，理由与 SessionFamilyTurnsSourceSQL 相同
//
// `session_bodies_hot`（近期热窗）与 `session_bodies`（月度分区父表）是两张表，
// 只读其一会漏掉另一侧的行。实测本地：hot 2,651 / parent 1,779,621，
// **两侧 request_id 交集为 0**（promote 会排空 hot），所以 UNION ALL 不会重复。
// ⚠ 但 `session_bodies` 父表内部有 57 行重复 request_id（37 个 id）——
// 实测**这 37 个都不在 session_turns 里**，所以今天不会让导出多出重复消息。
// 那个「今天不会」不是保证，由 admin 侧「bodies 源不得让 turn 重复」的门钉住。
//
// The returned source carries no alias — callers append one (e.g. `rb`).
func SessionFamilyBodiesSourceSQL() string {
	return "(SELECT " + sessionBodiesSourceProjection +
		" FROM public.session_bodies_hot sb" +
		" UNION ALL SELECT " + sessionBodiesSourceProjection +
		" FROM public.session_bodies sb)"
}

// sessionBodiesSourceProjection 与 SessionFamilyBodiesSourceSQL 共用同一份字面量，
// 拆出来是为了让**列集合成为可被引用的合同**（§9.231）。
//
// # 为什么要把列集合变成合同，而不是只写在注释里
//
// 这个源**不投影 `ts`**。于是「按 request_id 单键 JOIN」的读方可以直接换，
// 而 `ON rb.request_id = rl.request_id AND rb.ts = rl.ts` 的读方换过去会
// **解析期就报错**（42703）—— 那是好的，属响亮失败。
// 但「响亮」不等于「已登记」：审计 §9.231 实测 25 个 v1 bodies 读方里有
// 3 个是 ts 等值 JOIN（`admin/body_resolver.go`、`admin/compression_sessions.go`、
// `cmd/compression-bench/main.go`），它们**不能**用这个 helper。
// ⇒ 列集合必须能被别的包读到，才能让那道门按列名核对而不是按印象核对。
//
// ⚠ 别在这里加 `ts` 来「让大家都能用」：ts 在两侧的**相等率极低**
// （`admin/session_bodies_batch.go` 的文件头实测 99.85% 不等），
// 加上它会把一个解析期错误换成一个**静默的行数变化**。响亮失败优于静默改行。
var sessionBodiesSourceProjection = "sb.request_id, sb.request_delta AS request_body, " +
	"sb.response_delta AS response_body, sb.outbound_body"

// SessionFamilyBodiesSourceColumns 是 SessionFamilyBodiesSourceSQL 对外暴露的
// **全部**列名（已去掉 `sb.` 前缀与 `AS` 别名）。
//
// 它是 §9.231 那道门的判据来源。调用方（admin 包）用它在**源码文本**里核对
// 「这个读方用到的 bodies 列是否都在合同内」—— 不能靠数投影个数，
// 也不能靠「helper 里有这个字符串」。
var SessionFamilyBodiesSourceColumns = []string{
	"request_id", "request_body", "response_body", "outbound_body",
}

// MirrorDriftClassSQL classifies a V1 request_logs row into the three buckets
// the session mirror hook cares about. Kept as one expression so the aggregate
// and the breakdown cannot disagree — and so every consumer that needs to
// exclude "the hook would not have mirrored this" agrees on the definition.
//
// The two exclusion arms are a statement-by-statement transcription of the
// Go gates in internal/sessionv2mirror/hook.go — NOT an independent guess at
// "what looks internal". Divergence is not cosmetic: a row the hook
// deliberately skipped but this expression calls genuine_loss keeps s4_ready
// false forever and blocks the cutover that is actually safe.
//
//	internal_loopback ← hook.go:102 `!synthetic && IsInternalAutoEntry(entry)`
//	    IsInternalAutoEntry (telemetry/internal_loopback.go:23-39):
//	      1. requires is_auto_request IS TRUE (a NULL is NOT internal);
//	      2. request_type  ∈ {title_gen, summary};
//	      3. origin_actor  ∈ {auto-title-generator, auto-summary-generator,
//	                          session-summary};
//	      4. otherwise task_type IS NULL/'' (taskless auto entry).
//	    work_type is deliberately NOT an arm: the Go gate never reads it, so
//	    keying on it would mask real loss (is_auto_request=TRUE + task_type
//	    set + work_type='session_title' is a business turn the hook mirrors).
//	non_terminal ← hook.go:71 `!entry.Success && !isTerminalFailure(entry)`
//	    isTerminalFailure (hook.go:991-1002) is true when request_status ∈
//	    {failure, rate_limited} OR error_kind is non-empty; the row is
//	    mirrored when Success is true. Negating both yields the arm below.
//
// SSOT: this used to live as an unexported const in package main
// (cmd/gateway/dual_read_validator.go), which made it unreachable from admin.
// Duplicating it there would have put a 10-line classification under two
// definitions with no compiler link between them; the existing parity test
// (cmd/gateway/dual_read_class_parity_test.go) only guards the main copy.
//
// The fragment hardcodes the table alias `rl` — callers must alias their
// source that way. `... <> 'genuine_loss'` is the exclusion form.
const MirrorDriftClassSQL = `
CASE
  WHEN COALESCE(rl.is_auto_request, false)
       AND (   TRIM(COALESCE(rl.request_type, '')) IN (` + internaltraffic.GeneratorRequestTypesSQLList + `)
            OR TRIM(COALESCE(rl.origin_actor, '')) IN (` + internaltraffic.GeneratorActorsSQLList + `)
            OR TRIM(COALESCE(rl.task_type, '')) = '')            THEN 'internal_loopback'
  WHEN NOT COALESCE(rl.success, false)
       AND TRIM(COALESCE(rl.request_status, '')) NOT IN ('failure', 'rate_limited')
       AND TRIM(COALESCE(rl.error_kind, '')) = ''                 THEN 'non_terminal'
  ELSE 'genuine_loss'
END`
