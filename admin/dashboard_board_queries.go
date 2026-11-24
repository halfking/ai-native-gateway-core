package admin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/maas"
)

func (h *Handler) shouldUseBoardLogsFallback(ctx context.Context, tenantID string, tr boardTimeRange) bool {
	if tr.Days <= 1 {
		return false
	}
	where, args := boardMinuteWhere(tr, tenantID, 0)
	var minBucket sql.NullTime
	err := h.db.QueryRow(ctx, `
		SELECT MIN(bucket) FROM request_stats_minute WHERE `+where, args...).Scan(&minBucket)
	if err != nil || !minBucket.Valid {
		return true
	}
	return minBucket.Time.After(tr.Start.Add(boardStatsCoverageSlack(tr)))
}

func trendPointsCoverRange(tr boardTimeRange, points []boardTrendPoint) bool {
	if len(points) == 0 {
		return false
	}
	earliest, err := time.Parse(time.RFC3339, points[0].Bucket)
	if err != nil {
		return false
	}
	return !earliest.After(tr.Start.Add(boardStatsCoverageSlack(tr)))
}

// queryBoardSummary returns the board hero numbers (总请求数/Token/费用/积分/
// 成功率/均延迟) for the window, with the probe toggle.
//
// ## 2026-11-24 第二轮（owner 拍板）：主源换成**请求级日志**（整页同源）
//
// 背景：第一轮把趋势与饼图切到请求级日志后，「近30天」页签出现 **2.08 倍**的
// 页面内口径差（卡片/趋势读 usage_facts = 163,457，饼图读请求日志 = 331,373）。
// 根因不是切片写错，而是**两个宇宙的历史覆盖起点不同**：
//
//	usage_facts  的写入起点 = 2026-11-10 13:38（该时刻一次批量 DELETE 抹掉了
//	               2026-10-12~11-10 的 ≈35.2 万行真实业务事实，证据链见
//	               docs/12小时内修订审计-2026-11-24-0704.md §C）
//	请求级日志    的保留起点 = 更早（211 实测至少回溯到 2026-10-12）
//
// ⇒ 窗口跨过事实族起点时，卡片（少算）与饼图（全量）必然对不上；窗口没跨过
//   （近 7 天）则恰好相等 ⇒ **缺陷只在长窗口显形**，很容易被「7 天看着没问题」掩盖。
//
// owner 决策（2026-11-24）：**整页切请求级日志** ⇒ 英雄区、趋势、饼图三条读链
// 读同一张视图、同一终态集、同一探测谓词 ⇒ 整页一个宇宙，长短窗口一致。
//
// ★★★ 代价（必须如实记录，不是白拿的）：★★★
//
//	① 英雄区不再与「供应商对账」逐字相等（对账侧读 report_snapshots/usage_facts）。
//	   但对账侧同样受那 35.2 万行丢失影响 ⇒ 继续用事实族当看板真源，
//	   等于「用一个已知少算 2 倍的源当真相」。
//	② 探测谓词从 traffic_class（事实族列）换成 markers 谓词（日志列）⇒
//	   与对账的分面口径在边缘行上可能不同（详见 boardLogsWhereTerminal 的注释）。
//
// 回退链保持既有形状：请求级日志视图缺（42P01，未迁移环境）或查询失败时，
// exclude 模式落回旧分钟表口径（本文件下方 queryBoardSummaryMinute）——那是
// 修复前 board 一直在显示的数字，作为降级值不会比改前更差；include 模式没有
// 分钟表等价物（分钟表根本不含探测行），直接交给调用方的 logs fallback。
func (h *Handler) queryBoardSummary(ctx context.Context, tenantID string, tr boardTimeRange, includeProbes bool) (map[string]any, bool) {
	if s, ok := h.queryBoardSummaryLogs(ctx, tenantID, tr, includeProbes); ok {
		return s, true
	}
	if includeProbes {
		// 分钟表不含探测行，无法近似 include 口径；交给调用方的
		// fallbackBoardSummary 链（它会自带 degraded 标记）。
		return nil, false
	}
	return h.queryBoardSummaryMinute(ctx, tenantID, tr)
}

// queryBoardSummaryFacts reads the hero summary from usage_facts.
//
// Second return is false when the table is missing (42P01 → caller falls back)
// or the query failed for another reason (timeout etc. → same fallback: the
// minute path serves the pre-fix numbers, and a Warn is logged so the degrade
// boardLogsWhereTerminal 返回英雄区与趋势**共用**的 WHERE 片段
// （请求级日志 + 终态状态集 + 探测口径），并与**饼图主路径逐字同源**。
//
// ★★★ 2026-11-24 第二轮（★ owner 拍板「整页切请求级日志」）：★★★
// ★★★ 英雄区与趋势此前读 usage_facts、饼图读请求级日志 ⇒ **两个宇宙**。
// ★★★ 而这两个宇宙的**历史覆盖起点不同**（事实族的写入起点 vs 日志的保留起点），
// ★★★ 窗口一旦跨过事实族起点，同一页就会出现两个打架的总数
// ★★★ （生产实测 2026-11-24「近30天」页签：卡片 163,457 vs 饼图 331,373 ⇒ 差 2.08 倍；
// ★★★ 「近7天」页签因窗口全在覆盖期内而恰好相等 ⇒ **缺陷只在长窗口显形**）。
// ★★★ ⇒ 现在**英雄区、趋势、饼图三条读链全部读同一张请求级日志视图**，
// ★★★ 且共用本构造器 ⇒ 整页同一宇宙、同一谓词、同一终态集。
//
// ★ 关于探测谓词：请求级日志上没有 traffic_class 列 ⇒ 只能用 markers 谓词
// （bg.ProbeTrafficExclusionPredicateView，与饼图、降级路径逐字同源）。
// ★ ★★ 代价（必须如实记录）：traffic_class 与 markers 谓词是**两把尺**，
// ★ ★★ 在边缘行上可能分叉；但**事实族在 2026-11-10 13:37 丢失了 ≈35.2 万行**
// ★ ★★ （证据链见 docs/12小时内修订审计-2026-11-24-0704.md §C）⇒ 继续拿事实族
// ★ ★★ 当看板真源，等于「用一个已知少算了 2 倍的源当真相」⇒ 整页同源优先。
//
// ★★★ 为什么抽成共用构造器（★ 真源统一的结构性保证 ★★★）：
// ★★★ 「卡片 == Σ趋势桶」这个不变量**不能靠两边各写各的、再靠门比对文本**——
// ★★★ 那种写法一旦有人改了一边，门要么误报要么漏报。
// ★★★ 现在英雄区与趋势**必须**调用本函数 ⇒ 同表同谓词由**结构**保证，
// ★★★ 门只负责验证「真的都调了它」（admin/dashboard_board_truth_source_test.go 门 ③）。
func boardLogsWhereTerminal(tr boardTimeRange, tenantID string, includeProbes bool) (string, []any) {
	where, args := boardLogsWhereProbeScope(tr, "r", tenantID, includeProbes)
	where += " AND r.request_status IN ('success','failure','rate_limited')"
	return where, args
}

// boardCreditsExpr 返回看板（英雄区/趋势/饼图）**共用**的积分表达式。
//
// ★ 三条读链必须用**同一个**表达式，否则同一页的积分卡与各饼图切片的积分对不上。
// ★ includeDefaultTenant 的口径与既有降级路径一致（admin/dashboard_board_fallback.go）。
func boardCreditsExpr(tenantID string) string {
	return maas.RequestLogCreditsSQL("r", tenantID == "" || tenantID == "default")
}

// boardSummaryLogsSQL 返回英雄区汇总的 SQL（读请求级日志）。
//
// ★ token 口径不因换源而变：视图里 total_tokens = NULLIF(prompt+completion, 0)
//   （request_logs_with_current_month 的视图定义）⇒ 与 usage_facts 的
//   「total = prompt + completion」契约（stats/event.go）天然一致。
func boardSummaryLogsSQL(where, creditsExpr string) string {
	return boardSummaryLogsUnionSQL(where, creditsExpr, false)
}

func boardSummaryLogsUnionSQL(where, creditsExpr string, includeProbes bool) string {
	businessSQL := `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE r.request_status = 'success'),
			COALESCE(SUM(r.prompt_tokens), 0),
			COALESCE(SUM(r.completion_tokens), 0),
			COALESCE(SUM(r.total_tokens), 0),
			COALESCE(SUM(` + creditsExpr + `), 0),
			COALESCE(SUM(r.cost_usd), 0),
			COALESCE(SUM(r.latency_ms), 0)
		FROM request_logs_with_current_month r
		WHERE ` + where
	if !includeProbes {
		return businessSQL
	}
	// ★★ 2026-11-25：「含探测」时 UNION ALL 探测车道表（session_turns_probe_hot）。
	// ★★ 探测流量已物理隔离（迁移928，LLM_GATEWAY_PROBE_LANE_PHYSICAL=1）⇒
	// ★★ request_logs_with_current_month 视图**只读业务表**（session_turns / session_turns_hot）
	// ★★ ⇒ 结构上读不到探测行 ⇒ 「含探测/不含探测」切换后数据不变（生产实测211）。
	// ★★ 探测表缺列适配：request_status 由 success/status_code/error_kind 构造（与视图同款）；
	// ★★ cost_usd/identity_hash 探测表没有 ⇒ NULL；credits_charged 探测表有但看板不用它。
	probeSQL := `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE p.request_status = 'success'),
			COALESCE(SUM(p.prompt_tokens), 0),
			COALESCE(SUM(p.completion_tokens), 0),
			COALESCE(SUM(p.total_tokens), 0),
			0,
			0.0,
			COALESCE(SUM(p.latency_ms), 0)
		FROM (
			SELECT
				ts, tenant_id, provider_id, client_model, outbound_model, agent_name, client_ip,
				prompt_tokens, completion_tokens, total_tokens, latency_ms, error_kind,
				CASE
					WHEN success IS NULL THEN NULL
					WHEN success THEN 'success'
					WHEN error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited'
					WHEN status_code = 429 THEN 'rate_limited'
					ELSE 'failure'
				END AS request_status,
				NULL::numeric(18,8) AS cost_usd,
				NULL::text AS identity_hash
			FROM session_turns_probe_hot
		) p
		WHERE ` + where
	return `SELECT SUM(cnt), SUM(ok), SUM(pt), SUM(ct), SUM(tt), SUM(cr), SUM(cost), SUM(lat) FROM (
		SELECT * FROM (` + businessSQL + `) b(cnt,ok,pt,ct,tt,cr,cost,lat)
		UNION ALL
		SELECT * FROM (` + probeSQL + `) p(cnt,ok,pt,ct,tt,cr,cost,lat)
	) u`
}

// queryBoardSummaryLogs 从**请求级日志**算英雄区汇总（与饼图、趋势同一宇宙）。
//
// ★★★ 2026-11-24 第二轮：★ 此前读 usage_facts（对账真源），但事实族在
// ★★★ 2026-11-10 13:37 丢失 ≈35.2 万行 ⇒ 任何跨过该点的窗口，卡片都少算
// ★★★ （实测「近30天」页签少算 2.08 倍）⇒ owner 拍板整页切请求级日志。
//
// ★ 与饼图同源的三个要素：**同一张视图**、**同一个终态集**
// （request_status IN ('success','failure','rate_limited')）、**同一个探测谓词**
// （boardLogsWhereProbeScope ⇒ bg.ProbeTrafficExclusionPredicateView）。
//
// Second return is false when the query failed (missing view → 42P01, timeout,
// etc.) so the caller can fall back to the legacy minute path.
func (h *Handler) queryBoardSummaryLogs(ctx context.Context, tenantID string, tr boardTimeRange, includeProbes bool) (map[string]any, bool) {
	where, args := boardLogsWhereTerminal(tr, tenantID, includeProbes)

	var totalReq, successCnt, promptTok, compTok, totalTok, credits, latencySum int64
	var costUSD float64
	err := h.db.QueryRow(ctx, boardSummaryLogsUnionSQL(where, boardCreditsExpr(tenantID), includeProbes), args...).Scan(
		&totalReq, &successCnt, &promptTok, &compTok, &totalTok, &credits, &costUSD, &latencySum)
	if err != nil {
		if !IsMissingRelationError(err) {
			slog.Warn("board summary (request-level logs) failed, falling back to minute path",
				"tenant_id", tenantID, "start", tr.Start, "end", tr.End, "error", err)
		}
		return nil, false
	}
	if totalReq == 0 && !includeProbes {
		// 空窗口保持旧行为：分钟表还有一次机会（它可能在请求级日志尚未
		// 迁移的环境里有数据），空结果交回旧链路判定。
		return nil, false
	}

	var activeKeys, activeModels, providers int
	_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)

	successRate := 0.0
	if totalReq > 0 {
		successRate = float64(successCnt) / float64(totalReq)
	}
	avgLatency := 0.0
	if totalReq > 0 {
		avgLatency = float64(latencySum) / float64(totalReq)
	}

	return map[string]any{
		"total_requests":          totalReq,
		"total_prompt_tokens":     promptTok,
		"total_completion_tokens": compTok,
		"total_tokens":            totalTok,
		"total_cost_usd":          costUSD,
		"total_credits_charged":   credits,
		"success_rate":            successRate,
		"avg_latency_ms":          avgLatency,
		"active_api_keys":         activeKeys,
		"active_models":           activeModels,
		"providers":               providers,
		"summary_source":          "request_logs",
	}, true
}

// queryBoardSummaryMinute is the pre-2026-11-20 primary path, retained as the
// degraded fallback for environments where usage_facts is not migrated. It is
// business-only (probes are excluded by the minute-table writer) and can
// undercount across gateway restarts (bounded resweep) — both reasons it is no
// longer the primary. See queryBoardSummary's header comment.
func (h *Handler) queryBoardSummaryMinute(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, bool) {
	where, args := boardMinuteWhere(tr, tenantID, 0)

	var totalReq, successCnt, failCnt, promptTok, compTok, totalTok, credits, latencySum int64
	var costUSD float64
	err := h.db.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(success_count), 0),
			COALESCE(SUM(failure_count), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(latency_ms_sum), 0)
		FROM request_stats_minute
		WHERE `+where+`
	`, args...).Scan(
		&totalReq, &successCnt, &failCnt, &promptTok, &compTok, &totalTok, &credits, &costUSD, &latencySum,
	)
	if err != nil || totalReq == 0 {
		return nil, false
	}
	// 2026-08-31: when minute data has rows, use it as the authoritative
	// source. The previous version forced the fallback for days>1 even
	// when minute data was present (correctly covering recent days),
	// which made every days>1 board request run a 1s fallbackBoardSummary
	// aggregation on top of an already-fast minute pass. Keep the
	// fallback for the empty case only.

	var activeKeys, activeModels, providers int
	_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)

	successRate := 0.0
	if totalReq > 0 {
		successRate = float64(successCnt) / float64(totalReq)
	}
	avgLatency := 0.0
	if totalReq > 0 {
		avgLatency = float64(latencySum) / float64(totalReq)
	}

	return map[string]any{
		"total_requests":          totalReq,
		"total_prompt_tokens":     promptTok,
		"total_completion_tokens": compTok,
		"total_tokens":            totalTok,
		"total_cost_usd":          costUSD,
		"total_credits_charged":   credits,
		"success_rate":            successRate,
		"avg_latency_ms":          avgLatency,
		"active_api_keys":         activeKeys,
		"active_models":           activeModels,
		"providers":               providers,
	}, true
}

// fallbackBoardSummary 在 minute 统计不可用时走 _request_logs 聚合。
//
// ## 2026-10-03：原来这里 `_ = ...Scan(...)` 丢弃错误
//
// 后果不是「少了一个降级点」，而是**整屏数字都是 0 且完全无标记**：
// 请求数 0、Token 0、费用 $0.00。page 上看起来就是「这个时段没有流量」。
// 同文件的 fallbackBoardPies / fallbackBoardTrends / fallbackErrorDrill
// 都返回 error，只有这一个把错误吃掉了。
//
// 现在返回 (payload, err)；调用方据此打降级标记。
func (h *Handler) fallbackBoardSummary(ctx context.Context, tenantID string, tr boardTimeRange) (map[string]any, error) {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)

	var totalReq int64
	var promptTok, compTok sql.NullInt64
	var costUSD sql.NullFloat64
	var avgLatency sql.NullFloat64
	var successRate sql.NullFloat64
	scanErr := h.db.QueryRow(ctx, `
		SELECT COUNT(*),
			COALESCE(SUM(`+alias+`.prompt_tokens), 0),
			COALESCE(SUM(`+alias+`.completion_tokens), 0),
			COALESCE(SUM(`+alias+`.cost_usd), 0),
			COALESCE(AVG(`+alias+`.latency_ms) FILTER (WHERE `+alias+`.latency_ms IS NOT NULL), 0),
			COALESCE(AVG(CASE WHEN `+alias+`.success THEN 1.0 ELSE 0.0 END), 0)
		FROM `+logsTable+`
		WHERE `+where+` AND `+alias+`.request_status IN ('success', 'failure', 'rate_limited')
	`, args...).Scan(&totalReq, &promptTok, &compTok, &costUSD, &avgLatency, &successRate)

	// 主聚合查询失败 → 整屏数字都不可信。**仍然返回载荷**（保持既有
	// 「降级也返回 200」的形状），但必须带 degraded 让调用方/前端能区分。
	// 只有 42P01 归入本契约：那是「可迁移性缺失」；
	// 其它错误照旧上抛，由上层变成 500 —— 把超时说成「0 请求」是更坏的谎。
	if scanErr != nil && !IsMissingRelationError(scanErr) {
		return nil, scanErr
	}

	// 2026-10-03 的原始动机（上游注释，保留）：回退路径的积分与主路径的
	// request_stats_minute.credits_charged 曾是两个数据源，缺源时返回 0 却无标记。
	//
	// 本次改成 queryBoardCreditsExcludingProbes 而非 queryTotalCreditsCharged：
	//   · 同源 —— 主路径的 request_stats_minute.credits_charged 本身就是
	//     maas.RequestLogCreditsSQL 对 _request_logs 算出来的，回退路径读同源，
	//     1d/7d/30d 切页时数字才不会因为换源而跳变（上游注释指出的正是这个缺陷）；
	//   · 同口径 —— 其余读面（请求数/成功率/Token/费用）都经 boardLogsWhere 排除了
	//     探针，积分若仍取自计费账本（账本无探针标记、探针轮次是真的被扣费的），
	//     同一屏就会出现「Token 已排除探针、积分含探针」的自相矛盾。
	//
	// 降级契约不变：queryBoardCreditsExcludingProbes 与 queryTotalCreditsCharged
	// 同签名 (int64, string)，42P01 仍以 credits_missing_view 上报，前端仍显示「—」。
	credits, creditsDegradedView := h.queryBoardCreditsExcludingProbes(ctx, tenantID, tr)

	var activeKeys, activeModels, providers int
	_ = h.queryOverviewCounts(ctx, tenantID, tr, &activeKeys, &activeModels, &providers)

	payload := map[string]any{
		"total_requests":          totalReq,
		"total_prompt_tokens":     promptTok.Int64,
		"total_completion_tokens": compTok.Int64,
		"total_tokens":            promptTok.Int64 + compTok.Int64,
		"total_cost_usd":          costUSD.Float64,
		"total_credits_charged":   credits,
		"success_rate":            successRate.Float64,
		"avg_latency_ms":          avgLatency.Float64,
		"active_api_keys":         activeKeys,
		"active_models":           activeModels,
		"providers":               providers,
	}
	if scanErr != nil {
		// 42P01：主聚合查询缺表/缺视图，上面所有数字都是 0。
		// 这个载荷仍然是 200（保持既有降级形状），但必须自报家门。
		view := ExtractMissingRelationName(scanErr)
		ReportMissingRelation(slog.Default(), "boardSummaryFallback", scanErr)
		payload["degraded_summary"] = true
		payload["summary_missing_view"] = view
		payload["summary_hint"] = fmt.Sprintf(
			"数据源 %s 不可用，本页所有汇总数字（请求/Token/费用）均为 0，不可作为结论", view)
		// ⚠ 不在这里写 payload["degraded"] / payload["degraded_reason"]：
		// 那两个键在**载荷顶层**属于 board 整体（pies/trends，见
		// applyBoardDegradation），而这里的 summary 是其中一个子对象。
		// 2026-10-03 之前的写法让两种作用域共用一对键，于是
		//   · credits 降级会覆盖掉 summary 的整屏降级原因（后者先写）；
		//   · 前端 BoardHeroRow 的 creditsDegraded 读 summary.degraded，
		//     而整屏降级也会把它置 true ⇒ 积分卡显示「不可信」，
		//     但整屏明明有真实数字可显示。
		// 作用域不同的标记必须有不同的键，判据才有意义。
	}
	if creditsDegradedView != "" {
		payload["degraded"] = true
		payload["degraded_reason"] = "credits: " + creditsDegradedView
		payload["credits_missing_view"] = creditsDegradedView
		payload["credits_hint"] = missingRelationHint(creditsDegradedView)
	}
	return payload, nil
}

// queryBoardCreditsExcludingProbes computes the board's 总积分消耗 without
// falling back to the shared queryTotalCreditsCharged.
//
// Why the board cannot use queryTotalCreditsCharged: that helper reads
// maas_credit_consumption_buckets, the billing ledger. The ledger stores
// charged credits only — it carries no origin_stage / origin_actor /
// quality_flags / task_type, so probe traffic cannot be filtered out of it
// after the fact. Probe rounds really do get charged: the self-check key is
// created with tenant_id='default' (bg/self_check_worker.go:901) and
// shouldChargeUsage (domains/streaming/billing.go:9) inspects only token counts
// and failure stage, never the probe markers.
//
// So the board derives credits from _request_logs with the same exclusion every
// other board read face uses. That also makes this path AGREE with the minute
// rollup, which computes credits the same way (maas.RequestLogCreditsSQL over
// probe-filtered rows) — before this change the two paths answered the same
// field by different, non-comparable means.
//
// Scope (deliberate): billing stays untouched. queryTotalCreditsCharged is
// shared with the usage page (admin/usage.go:150) and still reports actual
// charged credits including probes; only the dashboard's operational view is
// probe-free. If probes should not be billable at all, that is a separate
// decision in shouldChargeUsage, not a statistics fix.
//
// Second return value mirrors queryTotalCreditsCharged's degraded-view
// contract (admin/usage_credits.go:creditDegradedView): 42P01 means "not
// computed", and the caller turns it into credits_missing_view so the board
// renders "—" instead of a fabricated 0. Returning a bare 0 here would
// reintroduce exactly the lie the upstream fix removed.
func (h *Handler) queryBoardCreditsExcludingProbes(ctx context.Context, tenantID string, tr boardTimeRange) (int64, string) {
	logsTable, alias := boardRequestLogsFromClause()
	where, args := boardLogsWhere(tr, alias, tenantID)
	where += " AND " + alias + `.request_status IN ('success', 'failure', 'rate_limited')`
	where += " AND " + requestLogsBillableClause(alias)

	creditsExpr := maas.RequestLogCreditsSQL(alias, tenantID == "" || tenantID == "default")
	var credits int64
	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(`+creditsExpr+`), 0)::bigint
		FROM `+logsTable+`
		WHERE `+where, args...).Scan(&credits); err != nil {
		// 42P01 → 「没算出来」，上报缺失视图让前端显示「—」；
		// 其它错误按 queryTotalCreditsCharged 的同款口径走 slog.Warn，
		// 不进 degraded 契约（契约只针对可迁移性缺失）。
		if IsMissingRelationError(err) {
			return 0, creditDegradedView(err)
		}
		slog.Warn("board credits (probe-excluded) failed",
			"tenant_id", tenantID,
			"start", tr.Start,
			"end", tr.End,
			"error", err)
		return 0, ""
	}
	return credits, ""
}

// overviewCountsNeedsLogFallback 判定「看板概览计数还要不要跑日志回退」。
//
// ## 2026-10-21（§143.37 / P20）：这是本轮加的门控，替代掉一个无条件回退
//
// ── 改的是什么 ────────────────────────────────────────────────────────
// 改前（`272e1bf93` 引入分钟路径时的形态）：
//
//	if err := h.queryOverviewCountsMinute(...); err != nil {
//	    if !IsMissingRelationError(err) {
//	        return err
//	    }
//	}
//	return h.fillOverviewCountsFromLogs(...)   // ★★ 在 if 块【外面】⇒ 无条件
//
// ★★ 那个 `return` 落在 `if` 块**外面** ⇒ **分钟路径成功时也照跑日志回退**。
//
//	（`272e1bf93` 的意图显然是「分钟路径当主、缺失时回退」，但收尾的 `return`
//	 被留在了 `if` 之外 ⇒ 快路径加上了，慢路径却从没被关掉。）
//
// ── 为什么这一扫在分钟路径成功时是【纯浪费】─────────────────────────────
// ★ `fillOverviewCountsFromLogs` 只在计数为 0 时才采纳它的结果：
//
//		if *models == 0 && logModels > 0 { *models = logModels }
//		if *providers == 0 && logProviders > 0 { *providers = logProviders }
//
//	  ⇒ 当分钟路径已经给出**两个非零**计数时，那一整扫（会话族 hot ∪ parent
//	    两腿 UNION ALL + 118 列投影 + details LEFT JOIN）的结果
//	    **被逐字丢弃** ⇒ 扫了、付了钱、什么也没换到。
//
// ★ 生产实测（§143.37.3，只读）：分钟路径在 1/7/30 天 × 平台/租户
//
//	**6 个组合里全部给出两个非零计数**（模型 79/165/488、供应商 8/10/19）
//	⇒ 6/6 组合下这一扫都是纯浪费。
//	⇒ 而它 2 天跑了 412 次、读了 21.8 GB 物理读、占了 1,108.7 s 的库时间。
//
// ── 语义【逐值】不变（不是「大概等价」）────────────────────────────────
// 设分钟路径结果为 (m, p)，日志回退结果为 (lm, lp)：
//
//	改前：恒执行日志回退 ⇒ 最终 (m==0&&lm>0 ? lm : m, p==0&&lp>0 ? lp : p)
//	改后：仅当 (m==0 || p==0) 才执行日志回退
//	      · (m!=0 && p!=0)：改前跑了但两个 if 都不成立 ⇒ 状态不变；
//	                        改后不跑 ⇒ 状态不变。★ 逐值相等。
//	      · (m==0 || p==0)：改前改后都执行日志回退 ⇒ 逐字相同的代码路径。
//
// ⚠ 诚实边界（唯一的差别，不是「零风险」）：改前在 (m!=0 && p!=0) 时
//
//	**可能把日志查询的错误返回给调用方**；改后返回 nil。
//	⇒ 两个调用点（`queryBoardSummary` / `fallbackBoardSummary`）都用 `_ =`
//	  丢弃了这个错误 ⇒ **调用点可观测行为不变**（由门
//	  `TestSection14337CallSitesDiscardOverviewCountsError` 钉住）。
//
// ⚠ 边界②：分钟路径报 42P01（关系缺失）时**仍然跑**日志回退 ——
//
//	那正是回退存在的理由（聚合表没装 ⇒ 只能从明细表算）。
//	非 42P01 的错误（超时等）仍然直接向上返回，不回退（同改前）。
func overviewCountsNeedsLogFallback(minuteErr error, models, providers int) bool {
	if minuteErr != nil {
		// 只有「关系缺失」才回退；其它错误由调用方直接返回（与改前一致）。
		return IsMissingRelationError(minuteErr)
	}
	// 分钟路径已成功 ⇒ 只在确实需要补 0 时才回退。
	return models == 0 || providers == 0
}

func (h *Handler) queryOverviewCounts(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	err := h.queryOverviewCountsMinute(ctx, tenantID, tr, keys, models, providers)
	if err != nil && !IsMissingRelationError(err) {
		return err
	}
	if !overviewCountsNeedsLogFallback(err, *models, *providers) {
		return nil
	}
	return h.fillOverviewCountsFromLogs(ctx, tenantID, tr, keys, models, providers)
}

func (h *Handler) queryOverviewCountsMinute(ctx context.Context, tenantID string, tr boardTimeRange, keys, models, providers *int) error {
	if tenantID != "" {
		return h.db.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM api_keys WHERE tenant_id = $1 AND enabled = TRUE),
				(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
				 WHERE dim_type = 'model' AND tenant_id = $1 AND bucket >= $2 AND bucket < $3),
				(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
				 WHERE dim_type = 'provider' AND tenant_id = $1 AND bucket >= $2 AND bucket < $3)
		`, tenantID, tr.Start, tr.End).Scan(keys, models, providers)
	}
	return h.db.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM api_keys WHERE enabled = TRUE),
			(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
			 WHERE dim_type = 'model' AND bucket >= $1 AND bucket < $2),
			(SELECT COUNT(DISTINCT dim_key) FROM request_stats_dim_minute
			 WHERE dim_type = 'provider' AND bucket >= $1 AND bucket < $2)
	`, tr.Start, tr.End).Scan(keys, models, providers)
}

// resolveBoardPies 解析看板饼图，第二个返回值是降级账本。
//
// 2026-10-03：以前只有 (pies, error)。42P01 走日志回退后，回退里每个维度
// 的失败都被吞成空数组，于是这条路径**永远不返回 error** —— 降级事实
// 在函数边界上就丢了，handler 无从知道该不该给页面打标记。
func (h *Handler) resolveBoardPies(ctx context.Context, tenantID string, tr boardTimeRange, includeProbes bool) (map[string]any, boardPieDegradation, error) {
	pies, degraded, err := h.queryBoardPies(ctx, tenantID, tr, includeProbes)
	if err != nil {
		if IsMissingRelationError(err) {
			return h.fallbackBoardPies(ctx, tenantID, tr)
		}
		return nil, boardPieDegradation{}, err
	}
	// 2026-08-31: only fall back to the slow log-based query when minute
	// stats have NO data for the range. Previously the code also fired
	// the fallback when minute data was present-but-incomplete, which
	// silently made every days>1 board request run 7 sequential pie
	// queries (~9.8s) over request_logs_with_current_month and exhaust
	// the handler's 10s timeout. The minute path is the authoritative
	// source for the operational dashboard; the log path is only a
	// safety net for environments where the minute rollup is missing.
	if piesEmpty(pies) && h.shouldUseBoardLogsFallback(ctx, tenantID, tr) {
		fb, fbDegraded, fbErr := h.fallbackBoardPies(ctx, tenantID, tr)
		if fbErr == nil {
			return fb, fbDegraded, nil
		}
	}
	return pies, degraded, nil
}

func piesEmpty(pies map[string]any) bool {
	if len(pies) == 0 {
		return true
	}
	for _, v := range pies {
		items, ok := v.([]boardPieItem)
		if ok && len(items) > 0 {
			return false
		}
	}
	return true
}

// boardPieDimTypes 列出看板饼图的 7 个维度。
//
// ★ dimType 用**维表（request_stats_dim_minute）里的原名**，respKey 是响应里的键名。
// ★★ 两者不可互换：降级路径（queryDimPie）按 dimType 去维表取，主路径
// ★★ （queryBoardPiesFacts）按同一套 dim_type 聚合请求级日志 ⇒ ★★ 两边产出同一套图例 key。
var boardPieDimTypes = []struct {
	dimType string
	respKey string
}{
	{"agent_name", "clients"},
	{"client_ip", "client_ips"},
	{"identity_hash", "identity_hashes"},
	{"model", "models"},
	{"error_kind", "errors"},
	{"tenant", "tenants"},
	{"provider", "providers"},
}

// boardPieDimKeyExprs 是 7 个维度的 dim_key 表达式，**逐字照抄**
// bg/stats_minute_rollup.go 的 rollupDimQueries。
//
// ★★ 为什么必须照抄（而不是各写各的）：★ dim_key 的取值形态是**维表与看板之间的契约**
// ★★ —— 模型名回落 client_model、provider 取 id 的文本、error_kind 取 slug、
// ★★ 空值折成 '__unknown__'、tenant 空值折成 'default'。
// ★★ 两边不一致 ⇒ 同一个页面在「主路径 → 降级路径」切换时图例全变 ⇒ 用户看到的是
// ★★ 「同一张图换了套名字」，而不是「数据变了」。
// ★★ ⇒ 由 TestBoardPieDimKeyExpressionsMatchRollup 钉住两侧不漂移。
var boardPieDimKeyExprs = map[string]string{
	"agent_name":    `COALESCE(NULLIF(r.agent_name,''), '__unknown__')`,
	"client_ip":     `COALESCE(NULLIF(HOST(r.client_ip),''), '__unknown__')`,
	"identity_hash": `COALESCE(NULLIF(r.identity_hash,''), '__unknown__')`,
	"model":         `COALESCE(NULLIF(r.outbound_model,''), NULLIF(r.client_model,''), '__unknown__')`,
	"tenant":        `COALESCE(NULLIF(r.tenant_id,''), 'default')`,
	"provider":      `COALESCE(r.provider_id, 0)::text`,
	"error_kind":    `COALESCE(NULLIF(r.error_kind,''), '__unknown__')`,
}

// boardPieDimTypeArraySQL 生成维度展开用的**常量数组**（顺序 = boardPieDimTypes）。
//
// ★★★ 2026-11-24 生产事故订正（★ 本轮第二道真缺陷 ★★★）：
// ★★★ **初版用的是 `CROSS JOIN LATERAL (VALUES …) AS d(dim_type, dim_key)`**，
// ★★★ 它在生产上**每次调用都报错**：
// ★★★   ERROR: input of anonymous composite types is not implemented (0A000)
// ★★★   LOCATION: record_in, rowtypes.c:103
// ★★★ 且**非确定性**（同一 SQL 有时过、有时炸）—— 根因不是数据，是**协议**：
// ★★★   · 生产走 pgx = **扩展协议 + 预备语句**；psql 简单协议**永不**触发；
// ★★★   · 预备语句在通用计划（generic plan）下，PG 必须把 LATERAL 子查询里
// ★★★     对**外层**的引用参数化；外层是**视图** RTE（request_logs_with_current_month），
// ★★★     整行引用 ⇒ **匿名 record 类型** ⇒ 生成 RECORD 类型的 Param ⇒
// ★★★     执行期调用 record_in ⇒ 报「匿名复合类型不支持」。
// ★★★   · ★★ 触发条件是「PG 这次选了哪个计划」★★：★ 生产上**连炸 5 次**
// ★★★     （11:48:32~11:48:55，各窗口各一条日志）；★ 随后在 211 上用**逐字等价**
// ★★★     的 SQL 手跑**连过 8 次**；★ 强制通用计划（SET plan_cache_mode =
// ★★★     force_generic_plan）复现时**时炸时不炸**（两份只差换行的 SQL，前者炸、
// ★★★     后者过）⇒ 统计信息/并行度/成本估算在变 ⇒ 坏计划时好时坏被选中。
// ★★★   · ★★ 推论（★ 本轮最重要的一条工程教训 ★）：★ **「psql 手测连过 8 次」
// ★★★     不足以否定生产上的红** ⇒ ★★ 凡是「PG 计划相关」的失败，执行型门禁
// ★★★     **没有确定性** ⇒ ★★ 确定性守卫只能靠**形状**（禁掉相关 LATERAL）⇒
// ★★★     ★★ 而本修法是**结构上消灭**这条路（无相关子查询 ⇒ 不可能有 RECORD Param）。
// ★★★ ⇒ 修法：**不用相关子查询**。改成 `CROSS JOIN unnest(ARRAY[…]) AS d(dim_type)`
// ★★★ （常量数组 ⇒ 函数扫描 ⇒ 无外层引用 ⇒ 无 RECORD Param）+ `CASE d.dim_type`
// ★★★ 取 dim_key ⇒ 计划期稳定，且**仍是单次扫描**（每行展开 7 个维度）。
// ★★★ 生产实测（211 只读）：LATERAL 形态在生产上连炸 5 次；unnest 形态
// ★★★ 在同库上 PREPARE / 显式 PREPARE / 强制通用计划各跑 3~8 次，**0 错**。
//
// ★★ 为什么仍然用「一次扫描 + 行展开」而不是「7 次独立查询」：
// ★★ bg/stats_minute_rollup.go 的 rollupDims 就是**7 次独立 INSERT = 7 次全表扫描**
// ★★ ⇒ 这正是 admin/dashboard_board_fallback.go 里注释所说的「7 次顺序饼图查询 ≈ 9.8s、
// ★★ 打爆 handler 的 10s 超时」的同款代价。
// ★★ 改成**单次扫描 + 行展开**后，生产实测（2026-11-24，211 只读）：
// ★★   7 天窗口 454ms、30 天窗口 1.74s ⇒ ★★ 比旧降级路径快 21 倍。
func boardPieDimTypeArraySQL() string {
	var b strings.Builder
	b.WriteString("ARRAY[")
	for i, d := range boardPieDimTypes {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("'")
		b.WriteString(d.dimType)
		b.WriteString("'")
	}
	b.WriteString("]")
	return b.String()
}

// boardPieDimKeyCaseSQL 生成按 dim_type 分派的 dim_key 表达式（CASE 形态）。
//
// ★ 表达式取自 boardPieDimKeyExprs（与 bg/stats_minute_rollup.go 的 rollupDimQueries
// ★ 逐字同源，跨文件一致性由 admin/dashboard_board_truth_source_test.go 门 ⑥ 钉住）。
// ★ 顺序用 boardPieDimTypes（**不用 map 迭代序**）⇒ SQL 文本确定 ⇒ 预备语句缓存可用。
func boardPieDimKeyCaseSQL() string {
	var b strings.Builder
	b.WriteString("CASE d.dim_type")
	for _, d := range boardPieDimTypes {
		b.WriteString("\n            WHEN '")
		b.WriteString(d.dimType)
		b.WriteString("' THEN ")
		b.WriteString(boardPieDimKeyExprs[d.dimType])
	}
	b.WriteString("\n           END")
	return b.String()
}

// boardPieFactsSQL 返回饼图主路径（请求级日志、单次扫描）的完整 SQL。
//
// ★ 单独抽成函数是为了让**真库门**能拿到**逐字同一份**语句去执行
// ★ （admin/dashboard_board_pie_sql_integration_test.go）——★ 生产事故证明：
// ★ 「SQL 文本看着对」与「PG 能执行它」是两件事。
func boardPieFactsSQL(where, creditsExpr string) string {
	return boardPieFactsUnionSQL(where, creditsExpr, false)
}

func boardPieFactsUnionSQL(where, creditsExpr string, includeProbes bool) string {
	businessSQL := `
			SELECT d.dim_type, ` + boardPieDimKeyCaseSQL() + ` AS dim_key, r.total_tokens, ` + creditsExpr + ` AS credits, r.cost_usd
			FROM request_logs_with_current_month r
			CROSS JOIN unnest(` + boardPieDimTypeArraySQL() + `) AS d(dim_type)
			WHERE ` + where + `
			  AND (d.dim_type <> 'error_kind' OR r.request_status = 'failure')
	`
	if !includeProbes {
		return `
		WITH dimmed AS (` + businessSQL + `
		), agg AS (
			SELECT dim_type, dim_key,
				COUNT(*) AS requests,
				COALESCE(SUM(total_tokens), 0) AS tokens,
				COALESCE(SUM(credits), 0) AS credits,
				COALESCE(SUM(cost_usd), 0) AS cost_usd
			FROM dimmed
			GROUP BY dim_type, dim_key
		), ranked AS (
			SELECT agg.*, ROW_NUMBER() OVER (PARTITION BY dim_type ORDER BY requests DESC, dim_key) AS rn
			FROM agg
		)
		SELECT dim_type, dim_key, requests, tokens, credits, cost_usd
		FROM ranked WHERE rn <= 25
		ORDER BY dim_type, requests DESC
	`
	}
	// ★★ 2026-11-25：「含探测」时 UNION ALL 探测车道表（同英雄区/趋势修法）。
	probeSQL := `
			SELECT d.dim_type, ` + boardPieDimKeyCaseSQL() + ` AS dim_key, p.total_tokens, 0 AS credits, 0.0 AS cost_usd
			FROM (
				SELECT
					ts, tenant_id, provider_id, client_model, outbound_model, agent_name, client_ip,
					prompt_tokens, completion_tokens, total_tokens, latency_ms, error_kind,
					CASE
						WHEN success IS NULL THEN NULL
						WHEN success THEN 'success'
						WHEN error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited'
						WHEN status_code = 429 THEN 'rate_limited'
						ELSE 'failure'
					END AS request_status,
					NULL::numeric(18,8) AS cost_usd,
					NULL::text AS identity_hash
				FROM session_turns_probe_hot
			) p
			CROSS JOIN unnest(` + boardPieDimTypeArraySQL() + `) AS d(dim_type)
			WHERE ` + where + `
			  AND (d.dim_type <> 'error_kind' OR p.request_status = 'failure')
	`
	return `
		WITH dimmed AS (` + businessSQL + `
			UNION ALL` + probeSQL + `
		), agg AS (
			SELECT dim_type, dim_key,
				COUNT(*) AS requests,
				COALESCE(SUM(total_tokens), 0) AS tokens,
				COALESCE(SUM(credits), 0) AS credits,
				COALESCE(SUM(cost_usd), 0) AS cost_usd
			FROM dimmed
			GROUP BY dim_type, dim_key
		), ranked AS (
			SELECT agg.*, ROW_NUMBER() OVER (PARTITION BY dim_type ORDER BY requests DESC, dim_key) AS rn
			FROM agg
		)
		SELECT dim_type, dim_key, requests, tokens, credits, cost_usd
		FROM ranked WHERE rn <= 25
		ORDER BY dim_type, requests DESC
	`
}

// queryBoardPiesFacts 从**请求级日志**算 7 个维度的饼图（单次扫描）。
//
// ★★ 这是 2026-11-24 真源统一轮的**主路径**：★ 过去 pies 读预聚合维表
// ★★ （request_stats_dim_minute），而那张表在写入时已剔除探测流量
// ★★ ⇒ ★★ 结构上无法响应 include_probes 开关 ⇒ ★★ 看板切「含探测」时
// ★★ 卡片与饼图口径分裂。本函数读请求级日志（探测标记仍在行上）⇒ ★★ 可按开关过滤。
//
// ★★ 为什么饼图不用 usage_facts（而英雄区用它）：★ usage_facts 的 39 列里
// ★★ **没有 agent_name / client_ip / identity_hash**，也没有 outbound_model/client_model
// ★★ ⇒ ★★ 那 7 个维度的唯一请求级来源就是请求日志表。
// ★★ ⇒ ★★ 「真源统一」在看板里的落点是：**英雄区 + 趋势读 usage_facts（同一张表、
// ★★ 同一探测谓词 ⇒ 逐项相等），饼图读请求日志（唯一有那 7 个维度的请求级源）**。
//
// ★★ 口径与维表逐条对齐（与 bg/stats_minute_rollup.go 的 rollupDims 同款）：
// ★★  ① request_status IN ('success','failure','rate_limited')（只算终态）
// ★★  ② error_kind 维度**只统计失败行**（否则成功行的空 error_kind 会变成
// ★★     '__unknown__' 大项 ⇒ 错误分布图会被成功量淹没）
// ★★  ③ dim_key 表达式逐字照抄（见 boardPieDimKeyExprs）
// ★★  ④ 探测排除谓词与 boardLogsWhereProbeScope 同源
//
// ★★ 生产实测对拍（211 只读，7 天窗口，排除探测）：★ 各维度请求数与维表差异
// ★★ 0.003% ~ 0.064%（2~4 笔的时间边界差）⇒ ★★ 切源不改变数字口径。
func (h *Handler) queryBoardPiesFacts(ctx context.Context, tenantID string, tr boardTimeRange, includeProbes bool) (map[string][]boardPieItem, error) {
	// ★ 与英雄区、趋势**共用** boardLogsWhereTerminal ⇒ 三条读链同表同谓词同终态集
	// （结构保证，不是靠两边各写各的）。
	where, args := boardLogsWhereTerminal(tr, tenantID, includeProbes)

	creditsExpr := boardCreditsExpr(tenantID)

	rows, err := h.db.Query(ctx, boardPieFactsUnionSQL(where, creditsExpr, includeProbes), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]boardPieItem, len(boardPieDimTypes))
	for rows.Next() {
		var dimType string
		var item boardPieItem
		if err := rows.Scan(&dimType, &item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD); err != nil {
			warnRowSkip("board pie (facts)", err)
			continue
		}
		out[dimType] = append(out[dimType], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// queryBoardPies 算 7 个维度的饼图。
//
// ★★ 第二个返回值是**降级账本**：★ 请求级主路径失败而落回预聚合维表时，
// ★★ 维表恒为排除探测口径 ⇒ ★★ 与英雄区的探测口径可能不一致 ⇒ ★★ 必须打降级标记，
// ★★ 否则「口径不同」会被静默吞掉（这正是本轮要消除的那类缺陷的镜像）。
func (h *Handler) queryBoardPies(ctx context.Context, tenantID string, tr boardTimeRange, includeProbes bool) (map[string]any, boardPieDegradation, error) {
	// ★ 主路径：请求级日志（单次扫描，探测口径可随 include_probes 切换）。
	if facts, err := h.queryBoardPiesFacts(ctx, tenantID, tr, includeProbes); err == nil {
		out := make(map[string]any, len(boardPieDimTypes))
		for _, d := range boardPieDimTypes {
			items := facts[d.dimType]
			if d.dimType == "provider" {
				items = h.resolveProviderPieLabels(ctx, items)
			}
			// Wave 3 B7 (2026-09-22) + R57 source fix: intranet keeps its raw
			// IP, public IPs collapse to 国家·省·市 through the local segment
			// table, and everything else degrades to the raw IP.
			if d.dimType == "client_ip" {
				items = classifyClientIPPie(items)
			}
			if items == nil {
				items = []boardPieItem{}
			}
			out[d.respKey] = items
		}
		return out, boardPieDegradation{}, nil
	} else {
		slog.Warn("board pies (request-level facts) failed, falling back to the minute dim table",
			"tenant_id", tenantID, "start", tr.Start, "end", tr.End, "error", err)
	}

	// ★ 降级路径：预聚合维表（queryDimPie）。★ 该表恒为排除探测口径 ⇒ 降级后
	// 饼图与英雄区的探测口径可能不一致 ⇒ 打降级标记（见函数头说明）。
	// 2026-09-03 (audit closure): the "clients" pie now aggregates by
	// agent_name (the canonical client-type identifier persisted on
	// _request_logs_hot.agent_name) instead of client_profile (the legacy
	// device-fingerprint column).
	types := map[string]string{
		"clients": "agent_name",
		// R57 B7: client_ips 饼图读真源 client_ip 维度（原 virtual_ips 读
		// identity 假名 10.x，GeoIP 归类对它不可达）。响应键同步改名。
		"client_ips":      "client_ip",
		"identity_hashes": "identity_hash",
		"models":          "model",
		"errors":          "error_kind",
		"tenants":         "tenant",
		"providers":       "provider",
	}
	out := make(map[string]any, len(types))
	degraded := boardPieDegradation{}
	for key, dimType := range types {
		items, err := h.queryDimPie(ctx, tenantID, tr, dimType)
		if err != nil {
			return nil, boardPieDegradation{}, err
		}
		if dimType == "provider" {
			items = h.resolveProviderPieLabels(ctx, items)
		}
		// Wave 3 B7 (2026-09-22) + R57 source fix: intranet keeps its raw
		// IP, public IPs collapse to 国家·省·市 through the local segment
		// table, and everything else degrades to the raw IP.
		if dimType == "client_ip" {
			items = classifyClientIPPie(items)
		}
		out[key] = items
		degraded.Keys = append(degraded.Keys, key)
	}
	sort.Strings(degraded.Keys)
	return out, degraded, nil
}

func (h *Handler) queryDimPie(ctx context.Context, tenantID string, tr boardTimeRange, dimType string) ([]boardPieItem, error) {
	args := []any{dimType, tr.Start, tr.End}
	where := "dim_type = $1 AND bucket >= $2 AND bucket < $3"
	if tenantID != "" {
		where += " AND tenant_id = $4"
		args = append(args, tenantID)
	}

	rows, err := h.db.Query(ctx, `
		SELECT dim_key,
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM request_stats_dim_minute
		WHERE `+where+`
		GROUP BY dim_key
		ORDER BY SUM(requests) DESC
		LIMIT 25
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []boardPieItem
	for rows.Next() {
		var item boardPieItem
		if err := rows.Scan(&item.Key, &item.Requests, &item.Tokens, &item.Credits, &item.CostUSD); err != nil {
			warnRowSkip("board pie", err)
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// resolveBoardTrends 解析趋势。
//
// ★★ 2026-11-24 真源统一轮：★ 主路径从「预聚合分钟表」改为「请求级事实表
// ★★ usage_facts」（与英雄区同表同探测谓词）⇒ ★★ include_probes 开关现在
// ★★ **同时联动英雄区与趋势图**（过去趋势恒为排除探测 ⇒ 卡片与趋势口径分裂）。
//
// ★★ 2026-11-24 第二轮（owner 拍板「整页切请求级日志」）：★★ ★ 主路径再改为
// ★★ ★ **请求级日志**（与英雄区、饼图同一张视图、同一终态集、同一探测谓词）
// ★★ ★ ⇒ 整页一个宇宙 ⇒ 「近30天」页签不再出现「卡片 163,457 vs 饼图 331,373」
// ★★ ★ 这种 2.08 倍的页面内分裂（成因：事实族在 2026-11-10 13:37 丢了 ≈35.2 万行，
// ★★ ★ 其覆盖起点与日志不同 ⇒ 证据链见 docs/12小时内修订审计-2026-11-24-0704.md §C）。
//
// ★★ 降级顺序：logs（请求级）→ minute（预聚合，恒排除探测）→ logs 明细回退。
// ★★ 后两条降级路径的探测口径与英雄区可能不同 ⇒ 打日志说明；整个看板在
// ★★ 请求级日志视图缺失时本来就走 legacy 链（英雄区也会降级）⇒ 属既有降级语义。
func (h *Handler) resolveBoardTrends(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64, includeProbes bool) ([]boardTrendPoint, error) {
	points, err := h.queryBoardTrendsLogs(ctx, tenantID, tr, providerID, includeProbes)
	if err != nil {
		if !IsMissingRelationError(err) {
			return nil, err
		}
		// 请求级日志视图未迁移 ⇒ 先试分钟表，再落日志。
		if minute, mErr := h.queryBoardTrendsMinute(ctx, tenantID, tr, providerID); mErr == nil && len(minute) > 0 {
			slog.Warn("board trends fell back to the minute rollup (probe scope may differ from the hero summary)",
				"tenant_id", tenantID, "start", tr.Start, "end", tr.End, "include_probes", includeProbes)
			return minute, nil
		}
		return h.fallbackBoardTrends(ctx, tenantID, tr, providerID)
	}
	if trendPointsCoverRange(tr, points) {
		return points, nil
	}
	// 2026-08-31: skip the log fallback when the minute path already has
	// ANY points. The fallback runs GROUP BY date_trunc on 175K rows of
	// request_logs_with_current_month, which takes ~1.4s on days=7 — when
	// added to other fallback work (summary, pies) it exhausts the 10s
	// handler timeout. Returning the partial minute data is the lesser
	// evil: the user sees a recent slice of trends instead of a 10s
	// timeout that returns trends=null and pies via fallback.
	if len(points) > 0 {
		return points, nil
	}
	if minute, mErr := h.queryBoardTrendsMinute(ctx, tenantID, tr, providerID); mErr == nil && len(minute) > 0 {
		return minute, nil
	}
	fallback, fbErr := h.fallbackBoardTrends(ctx, tenantID, tr, providerID)
	if fbErr != nil {
		if len(points) > 0 {
			return points, nil
		}
		return nil, fbErr
	}
	return fallback, nil
}

// boardTrendLogsSQL 返回趋势主路径的 SQL（读请求级日志，与英雄区/饼图同表同谓词）。
//
// ★ 单独抽成函数是为了让门禁能验证**实际执行的语句**（而不是去比对某个函数体里的
// ★ 源文本）——★ 2026-11-24 生产事故证明：源文本门会在 SQL 被重排/抽取后**误报**，
// ★ 而行为门测的是 PG 真正收到的东西。
func boardTrendLogsSQL(bucketExpr, where, creditsExpr string) string {
	return boardTrendLogsUnionSQL(bucketExpr, where, creditsExpr, false)
}

func boardTrendLogsUnionSQL(bucketExpr, where, creditsExpr string, includeProbes bool) string {
	businessSQL := fmt.Sprintf(`
		SELECT %s,
			COUNT(*),
			COALESCE(SUM(r.total_tokens), 0),
			COALESCE(SUM(%s), 0),
			COALESCE(SUM(r.cost_usd), 0)
		FROM request_logs_with_current_month r
		WHERE %s
		GROUP BY 1
	`, bucketExpr, creditsExpr, where)
	if !includeProbes {
		return businessSQL + "\n\t\tORDER BY 1 ASC"
	}
	// ★★ 2026-11-25：「含探测」时 UNION ALL 探测车道表（同英雄区修法）。
	probeSQL := fmt.Sprintf(`
		SELECT %s,
			COUNT(*),
			COALESCE(SUM(p.total_tokens), 0),
			0,
			0.0
		FROM (
			SELECT
				ts, tenant_id, provider_id, client_model, outbound_model, agent_name, client_ip,
				prompt_tokens, completion_tokens, total_tokens, latency_ms, error_kind,
				CASE
					WHEN success IS NULL THEN NULL
					WHEN success THEN 'success'
					WHEN error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited'
					WHEN status_code = 429 THEN 'rate_limited'
					ELSE 'failure'
				END AS request_status,
				NULL::numeric(18,8) AS cost_usd,
				NULL::text AS identity_hash
			FROM session_turns_probe_hot
		) p
		WHERE %s
		GROUP BY 1
	`, bucketExpr, where)
	return fmt.Sprintf(`
		SELECT bucket, SUM(cnt), SUM(tok), SUM(cr), SUM(cost)
		FROM (
			%s
			UNION ALL
			%s
		) u(bucket, cnt, tok, cr, cost)
		GROUP BY 1
		ORDER BY 1 ASC
	`, businessSQL, probeSQL)
}

// queryBoardTrendsLogs 从**请求级日志**算趋势分桶（与英雄区、饼图同一宇宙）。
//
// ★★★ 2026-11-24 第二轮：★★ ★ 此前读 usage_facts（第一轮从预聚合分钟表切过来）。
// ★★★ ★ 现在整页切请求级日志（owner 拍板）⇒ 三条读链同一张视图、同一终态集、
// ★★★ ★ 同一探测谓词 ⇒ 长短窗口都不再出现「卡片与饼图差 2 倍」。
//
// ★★ 过去趋势读预聚合表（request_stats_minute），而那张表在写入时已剔除探测流量
// ★★ ⇒ ★★ 结构上无法响应 include_probes ⇒ ★★ 看板切「含探测」时卡片变、趋势图不动。
func (h *Handler) queryBoardTrendsLogs(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64, includeProbes bool) ([]boardTrendPoint, error) {
	// ★ 与英雄区**共用** boardLogsWhereTerminal ⇒ 同表同谓词由结构保证（不是靠两边各写各的）。
	where, args := boardLogsWhereTerminal(tr, tenantID, includeProbes)
	if providerID > 0 {
		where += fmt.Sprintf(" AND r.provider_id = $%d", len(args)+1)
		args = append(args, providerID)
	}
	bucketExpr := sqlTrendBucket("r.ts", tr.trendBucketMinutes())

	rows, err := h.db.Query(ctx, boardTrendLogsUnionSQL(bucketExpr, where, boardCreditsExpr(tenantID), includeProbes), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []boardTrendPoint
	for rows.Next() {
		var p boardTrendPoint
		var bucket time.Time
		if err := rows.Scan(&bucket, &p.Requests, &p.Tokens, &p.Credits, &p.CostUSD); err != nil {
			warnRowSkip("board trends (facts)", err)
			continue
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

// queryBoardTrendsMinute 是趋势的**降级路径**：读预聚合分钟表。
//
// ★★ 该表在写入时已剔除探测流量 ⇒ ★★ 恒为排除探测口径 ⇒ ★★ 降级后趋势与英雄区
// ★★ 可能不同口径 ⇒ 由 applyBoardDegradation 打降级标记。
func (h *Handler) queryBoardTrendsMinute(ctx context.Context, tenantID string, tr boardTimeRange, providerID int64) ([]boardTrendPoint, error) {
	where, args := boardMinuteWhere(tr, tenantID, 0)
	if providerID > 0 {
		where += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, providerID)
	}
	bucketExpr := sqlTrendBucket("bucket", tr.trendBucketMinutes())

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s,
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(credits_charged), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM request_stats_minute
		WHERE %s
		GROUP BY 1
		ORDER BY 1 ASC
	`, bucketExpr, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []boardTrendPoint
	for rows.Next() {
		var p boardTrendPoint
		var bucket time.Time
		if err := rows.Scan(&bucket, &p.Requests, &p.Tokens, &p.Credits, &p.CostUSD); err != nil {
			warnRowSkip("board trends", err)
			continue
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}
