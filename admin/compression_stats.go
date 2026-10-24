package admin

// NOTE: All SELECTs on tenant-scoped tables (_request_logs etc.) in this file
// rely on tenantLogsClause() (admin/session_tenant.go) to inject
// "AND tenant_id = $N" for tenant_admin callers on non-default tenants.
// Super-admin / legacy admin_key / default-tenant callers see all rows.
//
// ★ R77 轮（2026-10-24，生产 252 共享库实测）改造要点：
//
//	① bodies 腿从 LEFT JOIN 改成 EXISTS 半连接（只问「有没有」，不搬载荷）
//	   ⇒ 生产实测：agg **1,355ms → 416ms**、bucket **517ms → 237ms**，且**逐行等价**
//	   ⇒ 顺带**免疫行放大**（bodies 源 = hot ∪ 6 个分区，同一个 request_id
//	   落在 hot 与某个分区里就会放大；实测当前窗口放大 = 0，但**不是被强制的约束**）。
//	② EXISTS **只许求值一次**（放进子查询的投影里，外层聚合直接引用）
//	   ⇒ 实测「求值两次」= 762ms（**正好翻倍**）⇒ 这是本轮最容易回退的一处。
//	③ 昂贵且可选的 estimated-orig 查询**排到最后**并给**独立预算上限**，
//	   拿不到时**显式标注 degraded**（仓内既有约定：admin/dashboard_board_aux.go）
//	   ⇒ 不再「静默吞错 ⇒ 200 + 残缺响应体」。
//	④ CompressedTotal += withOutbound（★ 当前与 `+= cnt` **逐字等价**
//	   —— 实测窗口内 with_outbound == cnt == 全部行；这是**结构性**修正，不是修当前 bug）。
//
// ★ 诚实边界（必须一起读）：③ 那条查询在 24h 窗口上**结构性不可得**
// （实测单独要 **34.3s**，因为它要读窗口内**全部** request_body 载荷
// —— 24h 窗口共 **1,278 MB** 存储字节）。
// ⇒ ★ **它不能靠「摘要化」解决**：CO-5 digest 模式被**刻意关闭**
// （domains/hooks/observability/telemetry/body_summary.go：「persisted false values
// survived the default change and continued **destroying the only complete audit
// copy of large requests**」）⇒ ⇒ ⇒ **审计完整性这条否决理由必须一并带着**，
// 不要在下一轮又把它当成一条「没试过的优化」。
// ⇒ ⇒ 要让它在 24h 窗口可得，只有两条路：① **改口径**（用 pg_column_size 取压缩后
// 体积 —— 数值含义变，**需属主裁决**）；② **后台预计算**（架构 + schema 变更）。
// ⇒ ⇒ ★ 本轮**两条都不擅自做**：端点改为「拿不到就显式说拿不到」。

import (
	"context"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type hourBucket struct {
	Hour       time.Time `json:"hour"`
	Total      int       `json:"total"`
	Compressed int       `json:"compressed"`
	Rate       float64   `json:"rate"`
}

const (
	// compressionStatsTotalBudget 端点总预算（不变：10s）。
	compressionStatsTotalBudget = 10 * time.Second

	// compressionStatsOptionalBudget 是「可选且昂贵」那一步的**独立预算上限**。
	//
	// ★ 为什么要有上限：estimated-orig 那条查询要读窗口内**全部** request_body
	//   载荷并做 SUM ⇒ 生产实测 24h 窗口 **34.3s**（端点总预算的 3.4×）
	//   ⇒ 在不改口径、不改存储的前提下，它在 24h 窗口上**结构性不可得**。
	// ⇒ ★ 预算上限的作用**不是**「让它跑完」，而是：
	//   ① 它**吃不掉**其余三条必需查询的预算（所以它必须**排到最后**）；
	//   ② 它拿不到时**显式标注**（degraded / degraded_reason）而不是静默消失。
	// ★ 生产实测标度：**1h 窗口该查询只要 938ms** ⇒ 4s 预算覆盖 ≤4h 窗口。
	compressionStatsOptionalBudget = 4 * time.Second
)

// compressionStatsResult 是端点的响应体。
//
// ★ R77 轮把它从匿名 struct 提成命名类型 ⇒ 形状门能直接构造它来测行为。
type compressionStatsResult struct {
	TotalRequests        int            `json:"total_requests"`
	CompressedTotal      int            `json:"compressed_total"`
	CompressionRate      float64        `json:"compression_rate"`
	StrategyDistribution map[string]int `json:"strategy_distribution"`
	// 2026-08-19: token-band observability counters (preliminary / forced
	// vs the below baseline). Below-band rows also count for transparency
	// so the band ratios are derivable; only the two non-baseline bands
	// are surfaced as separate numeric fields for alert wiring.
	TokenBandBelow       *int64       `json:"token_band_below,omitempty"`
	TokenBandPreliminary *int64       `json:"token_band_preliminary,omitempty"`
	TokenBandForced      *int64       `json:"token_band_forced,omitempty"`
	TotalOutboundTokens  *int64       `json:"total_outbound_tokens,omitempty"`
	EstimatedOrigTokens  *int64       `json:"estimated_original_tokens,omitempty"`
	EstimatedTokensSaved *int64       `json:"estimated_tokens_saved,omitempty"`
	SummaryModeRows      *int64       `json:"summary_mode_rows,omitempty"`
	HourlySeries         []hourBucket `json:"hourly_series"`
	// ★ R77 轮：**恒发**（无 omitempty）—— 前端要能区分
	// 「真的是 0 次」与「没查出来」（仓内既有约定：admin/dashboard_board_aux.go）。
	// ⇒ ★ 旧行为是「ctx 一过期就把后面的查询结果静默丢掉、仍然返回 200」
	//   ⇒ 调用方无从分辨「真的是 0」与「超时了」。
	Degraded bool `json:"degraded"`
	// ★ R77 轮：哪一步不可得（人可读）。
	DegradedReason string `json:"degraded_reason,omitempty"`
}

// compressionStatsStep 是端点里的一条查询步骤。
//
// ★ 为什么把「步骤」做成数据而不是直接顺序调用：
//
//	① **顺序必须可被门断言**（「昂贵且可选的那条必须排在最后」是本轮的核心不变量）；
//	② **预算必须可被门断言**（每一步的上限是数据，不是散落在代码里的常量）。
type compressionStatsStep struct {
	name string
	// budget 是该步的**独立预算上限**；0 = 用端点总预算的剩余部分（不限）。
	budget time.Duration
	// run 执行该步。返回 error 即「这一步的数据不可得」。
	run func(ctx context.Context) error
}

// compressionStatsStepResult 是单步的执行结果。
type compressionStatsStepResult struct {
	name string
	err  error
	// skipped = 该步因**预算耗尽**而**没有执行**（与「执行了但失败」是两回事，
	// 两者都要记账，但原因不同）。
	skipped bool
}

// compressionStatsRunSteps 按顺序跑各步，每步有自己的预算上限。
//
// ★ 三条纪律（R77 轮）：
//
//	① 预算 = min(该步上限, 端点剩余时间)
//	   ⇒ ★ 一条慢查询**吃不掉**后面的步骤的预算；
//	② 拿不到就**显式记账**（degraded + reason）⇒ **绝不静默消失**
//	   （旧行为：ctx 一过期，后面的查询全部 `slog.Warn` 后继续 ⇒ 200 + 残缺响应体）；
//	③ 步骤的**顺序是数据** ⇒ 可被门断言。
func compressionStatsRunSteps(ctx context.Context, steps []compressionStatsStep) []compressionStatsStepResult {
	results := make([]compressionStatsStepResult, 0, len(steps))
	for _, step := range steps {
		res := compressionStatsStepResult{name: step.name}

		// ★ 预算语义：`step.budget == 0` = **不限**（用父 ctx 的剩余时间）。
		//   ⚠ 父 ctx **没有 deadline** 时（context.Background()）也要尊重这条约定 ——
		//   否则「不限」会被实现成「跳过」⇒ 全部步骤都不跑（这条真 bug 是被
		//   admin/compression_stats_shape_test.go 的记账腿逮到的）。
		budget := step.budget
		useTimeout := budget > 0
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				// ★ 端点总预算已耗尽 ⇒ 这一步**没有执行**（不是执行失败）。
				res.skipped = true
				res.err = context.DeadlineExceeded
				results = append(results, res)
				continue
			}
			if !useTimeout || budget > remaining {
				budget = remaining
				useTimeout = true
			}
		}

		stepCtx := ctx
		cancel := func() {}
		if useTimeout {
			stepCtx, cancel = context.WithTimeout(ctx, budget)
		}
		started := time.Now()
		res.err = step.run(stepCtx)
		elapsed := time.Since(started)
		cancel()

		// ★ R78 轮：**每一步的耗时都要留痕**。
		//   为什么必须有：端点总耗时 = 各步之和，而共享库上的尾延迟让单步耗时
		//   在 40ms ~ 106s 之间波动（生产实测 n=119）⇒ 没有逐步日志就只能靠
		//   「掐断读数」猜 ⇒ R77 轮就因为缺它而误判出「应用侧 5.4s 缺口」。
		slog.Info("compression_stats step",
			"step", step.name,
			"elapsed_ms", elapsed.Milliseconds(),
			"budget_ms", budget.Milliseconds(),
			"skipped", res.skipped,
			"err", res.err)

		if res.err != nil {
			slog.Warn("compression_stats step failed", "step", step.name, "budget", budget, "error", res.err)
		}
		results = append(results, res)
	}
	return results
}

// compressionStatsDegradedReason 把步骤结果汇总成一句人能读的原因。
//
// ★ 空串 = 没降级（全部步骤都拿到了数据）。
func compressionStatsDegradedReason(results []compressionStatsStepResult) string {
	var parts []string
	for _, r := range results {
		if r.err == nil {
			continue
		}
		if r.skipped {
			parts = append(parts, r.name+" 未执行（预算耗尽）")
			continue
		}
		parts = append(parts, r.name+" 不可得")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "；")
}

// compressionStatsScanFailure 统一处理**行级**扫描失败。
//
// ★ 为什么抽成共用函数（2026-10-24 12h 审计轮 · 问题⑲）：
// R77/R78 轮给 agg 步加了 slog.Warn，而 band / bucket 两步仍是「静默 continue」
// ⇒ 同一条命令的三步里，**有两步的行级失败不留任何痕迹**
// ⇒ 端点照样 200、degraded=false，而 token_band_* / hourly_series 是**少算的**
// ⇒ ★ 调用方无法区分「真的是 0」与「这行没读出来」——
// 这与 R77 轮在**步级**修掉的是同一个坑，只是下移到了**行级**。
//
// ★ 本函数只做「让它出声」：**不改响应契约**
// （degraded / degraded_reason 的语义一个字不动）；
// 「行级损失要不要上报到响应里」属**契约变更** ⇒ 登记为待裁决点。
//
// 返回 true = 这一行应被跳过（调用方 continue）。
func compressionStatsScanFailure(step string, err error) bool {
	slog.Warn("compression_stats "+step+" scan failed", "error", err)
	return true
}

// compressionStatsSQLs 持有端点的 4 段查询 SQL。
type compressionStatsSQLs struct {
	agg     string // 策略分布 + token 汇总（必需）
	band    string // token_band 计数（便宜）
	bucket  string // 小时桶（必需：hourly_series）
	estOrig string // 原始 token 估计（可选 + 昂贵）
}

// compressionStatsSQL 产出 4 段查询的 SQL。
//
// ★ 为什么单独抽成函数：形状门必须断言**真正发给数据库的那个 SQL**，
// 而不是源码里的字面量片段 ⇒ 否则「换个地方写同样的 SQL」就会让门失去牙
// （R76 轮同款做法：admin/compression_sessions.go 的 compressionSessionsSQL）。
//
// ★ R77 轮的两条关键形状（生产实测，见 docs/audit/2026-10-24-R77-*.md）：
//
//	① bodies 腿必须是 EXISTS 半连接，且 **EXISTS 只许出现一次**
//	   （求值两次 = 762ms，正好是求值一次 416ms 的两倍）；
//	② 窗口谓词必须**下推进子查询**⇒ EXISTS 只对窗口内的行求值。
func compressionStatsSQL(tenantWhere, bucketExpr string) compressionStatsSQLs {
	turns := dbpkg.SessionFamilyTurnsSourceSQL()
	bodies := dbpkg.SessionFamilyBodiesSourceSQL()

	// ★ EXISTS 只求值一次：它算在子查询的投影里，外层聚合直接引用该列。
	//   （放在 WHERE / FILTER / CASE 里各写一遍 ⇒ 求值两次 ⇒ 实测成本翻倍。）
	hasOutbound := `EXISTS (SELECT 1 FROM ` + bodies +
		` rb WHERE rb.request_id = t.request_id AND rb.outbound_body IS NOT NULL)`

	// 窗口谓词：下推进子查询（t.*），外层不再重复过滤。
	windowPred := `t.ts >= $1 AND t.ts <= $2 AND ($3 OR t.success)`

	aggSQL := `
		SELECT
			COALESCE(NULLIF(rl.compression_strategy,''), 'none') AS strategy,
			COUNT(*) AS cnt,
			COUNT(*) FILTER (WHERE rl.has_outbound)::bigint AS with_outbound,
			SUM(COALESCE(rl.outbound_token_est, 0))::bigint AS total_tok_after,
			SUM(CASE WHEN rl.has_outbound THEN COALESCE(rl.outbound_token_est, 0) ELSE 0 END)::bigint AS compressed_tok
		FROM (
			SELECT t.ts, t.compression_strategy, t.outbound_token_est,
			       ` + hasOutbound + ` AS has_outbound
			FROM ` + turns + ` t
			WHERE ` + windowPred + tenantWhere + `
		) rl
		GROUP BY strategy
		ORDER BY cnt DESC`

	bucketSQL := `
		SELECT ` + bucketExpr + ` AS bucket,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE rl.has_outbound)::int AS compressed
		FROM (
			SELECT t.ts,
			       ` + hasOutbound + ` AS has_outbound
			FROM ` + turns + ` t
			WHERE ` + windowPred + tenantWhere + `
		) rl
		GROUP BY bucket
		ORDER BY bucket`

	// ⚠ band 查询读的是【会话族两臂视图】（request_logs_with_current_month，
	//   迁移 890 之后它 = session_turns_hot ∪ session_turns + details LEFT JOIN，
	//   不是 v1 族）⇒ 这一条**没有** bodies 腿，也就没有 EXISTS 的问题。
	bandSQL := `
		SELECT
			COALESCE(token_band, '') AS band,
			COUNT(*) AS cnt
		FROM request_logs_with_current_month
		WHERE ts >= $1 AND ts <= $2
		  AND ($3 OR success)` + tenantWhere + `
		GROUP BY token_band
		ORDER BY cnt DESC`

	// ★ 这一条**必须**读载荷（rb.request_body）⇒ **必须**保留 LEFT JOIN
	//   （半连接只能回答「有没有」，回答不了「正文多长」）。
	//   ⇒ 它的成本是**固有的**（24h 窗口 = 1,278 MB 载荷）⇒
	//   ⇒ 故它的对策是「排到最后 + 独立预算 + 显式标注」，**不是**改它的 SQL。
	//
	// ⚠ 2026-10-05：这里是运行时求值不是编译期常量 —— **函数调用不能出现在 const 声明里**
	//   （编译器报 "is not constant"）⇒ 别试图把它「优化」回 const。
	estOrigSQL := `
		SELECT
			COALESCE(SUM(CEIL(CASE
					WHEN jsonb_typeof(rb.request_body->'_gw_body_summary') = 'object'
						AND (rb.request_body #>> '{_gw_body_summary,bytes}') ~ '^[0-9]+$'
					THEN (rb.request_body #>> '{_gw_body_summary,bytes}')::numeric
				ELSE LENGTH(COALESCE(rb.request_body::text, ''))::numeric
			END / 4.0)), 0)::bigint,
			COALESCE(SUM(CASE WHEN jsonb_typeof(rb.request_body->'_gw_body_summary') = 'object' THEN 1 ELSE 0 END), 0)::bigint
		FROM ` + turns + ` rl
		LEFT JOIN ` + bodies + ` rb
		  ON rb.request_id = rl.request_id
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND ($3 OR rl.success)` + tenantWhere

	return compressionStatsSQLs{agg: aggSQL, band: bandSQL, bucket: bucketSQL, estOrig: estOrigSQL}
}

func (h *Handler) handleCompressionStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	hours := queryInt(r, "hours", 24)
	if hours < 1 {
		hours = 1
	}
	if hours > 720 {
		hours = 720
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	ctx, cancel := context.WithTimeout(r.Context(), compressionStatsTotalBudget)
	defer cancel()

	var from, to time.Time
	if fromStr != "" {
		var err error
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid from")
			return
		}
	} else {
		to = time.Now().UTC()
		from = to.Add(-time.Duration(hours) * time.Hour)
	}
	if toStr != "" {
		var err error
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid to")
			return
		}
	} else if fromStr != "" {
		to = time.Now().UTC()
	}

	tenantFilter := IsTenantAdmin(r)

	result := &compressionStatsResult{
		StrategyDistribution: make(map[string]int),
		HourlySeries:         make([]hourBucket, 0),
	}

	aggArgs := []any{from, to, !tenantFilter}
	aggArgIdx := 4
	tenantFrag, tenantArgs, _ := tenantLogsClause(r, aggArgIdx)
	aggWhere := ""
	if tenantFrag != "" {
		aggWhere = tenantFrag
		aggArgs = append(aggArgs, tenantArgs...)
	}

	rangeHours := to.Sub(from).Hours()
	var bucketExpr string
	switch {
	case rangeHours <= 48:
		bucketExpr = "date_trunc('hour', rl.ts)"
	case rangeHours <= 168:
		bucketExpr = "date_trunc('day', rl.ts) + INTERVAL '6 hours' * (EXTRACT(HOUR FROM rl.ts)::integer / 6)"
	default:
		bucketExpr = "date_trunc('day', rl.ts)"
	}

	queries := compressionStatsSQL(aggWhere, bucketExpr)

	steps := h.compressionStatsSteps(queries, aggArgs, result)

	results := compressionStatsRunSteps(ctx, steps)

	// ★ 核心数字（total_requests / compression_rate）不可得 ⇒ 端点整体不可信
	//   ⇒ 直接 500（与旧行为一致：那一步失败旧代码也是 500）。
	for _, res := range results {
		if res.name == "agg" && res.err != nil {
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
	}
	// 其余步骤不可得 ⇒ **200 + 显式降级标注**（前端据此区分「真的是 0」与「没查出来」）。
	if reason := compressionStatsDegradedReason(results); reason != "" {
		result.Degraded = true
		result.DegradedReason = reason
	}

	writeJSON(w, http.StatusOK, result)
}

// compressionStatsSteps 产出端点的步骤序列。
//
// ★ 为什么提成方法（而不是在 handler 里就地写 slice）：
//
//	⇒ **步骤的顺序与每步的预算是本轮的核心不变量**，必须**可被门断言**：
//	「昂贵且可选的那条必须排在最后」「它必须有独立且小于总预算的上限」。
func (h *Handler) compressionStatsSteps(queries compressionStatsSQLs, aggArgs []any, result *compressionStatsResult) []compressionStatsStep {
	// ★ R78 轮：顺序 = **成本升序**（band 40ms → bucket 220ms → agg 280ms → est-orig >10s）。
	//
	//   为什么必须这样排：生产 252 共享库实测，agg 那条查询的**耗时分布**是
	//   p50 ≈ 2.3s / p90 ≈ 21.9s / max ≈ 106s（n=119，慢查询日志窗口 4.2h）
	//   ⇒ 它**经常**吃满端点总预算 ⇒ 排在它后面的 band/bucket **一个都跑不到**
	//   ⇒ hourly_series / token_band 整段缺失（2026-10-24 生产实测）。
	//   ⇒ 而 band/bucket 本身**极便宜**（隔离态实测 40ms / 220ms）⇒ 前置它们
	//   只花掉 ~260ms，agg 仍剩 ~9.7s ⇒ **agg 的成功率几乎不变**。
	//
	//   ★ 这**不是**「给必需步加独立预算」：那会让必需步**被跳过**（等于核心数字缺失），
	//   与 R77 形状门第②条的立意相反 ⇒ 正确形态是**调顺序**（顺序是数据），
	//   而不是给必需步加预算（那会让它们变成可跳过的）。
	//
	//   ★ 代价（诚实记录）：若 band/bucket 在争用下变慢，agg 的可用时间会相应减少
	//   ⇒ agg 的 500 率可能略升 ⇒ 这是「用少量 agg 失败率换必需数据可得」的取舍；
	//   是否要把 agg 的失败也降级成 200+degraded ⇒ **属主裁决**（本轮不改）。
	return []compressionStatsStep{
		{
			name: "band",
			run: func(stepCtx context.Context) error {
				bandRows, err := h.db.Query(stepCtx, queries.band, aggArgs...)
				if err != nil {
					return err
				}
				defer bandRows.Close()
				for bandRows.Next() {
					var band string
					var cnt int
					if err := bandRows.Scan(&band, &cnt); err != nil {
						compressionStatsScanFailure("band", err)
					}
					switch band {
					case "below":
						v := int64(cnt)
						result.TokenBandBelow = &v
					case "preliminary":
						v := int64(cnt)
						result.TokenBandPreliminary = &v
					case "forced":
						v := int64(cnt)
						result.TokenBandForced = &v
					}
				}
				return bandRows.Err()
			},
		},
		{
			name: "bucket",
			run: func(stepCtx context.Context) error {
				bucketRows, err := h.db.Query(stepCtx, queries.bucket, aggArgs...)
				if err != nil {
					return err
				}
				defer bucketRows.Close()
				for bucketRows.Next() {
					var b struct {
						Bucket     time.Time
						Total      int
						Compressed int
					}
					if err := bucketRows.Scan(&b.Bucket, &b.Total, &b.Compressed); err != nil {
						compressionStatsScanFailure("bucket", err)
					}
					var rate float64
					if b.Total > 0 {
						rate = float64(b.Compressed) / float64(b.Total)
					}
					result.HourlySeries = append(result.HourlySeries, hourBucket{
						Hour:       b.Bucket,
						Total:      b.Total,
						Compressed: b.Compressed,
						Rate:       rate,
					})
				}
				return bucketRows.Err()
			},
		},
		{
			name: "agg",
			run: func(stepCtx context.Context) error {
				rows, err := h.db.Query(stepCtx, queries.agg, aggArgs...)
				if err != nil {
					return err
				}
				defer rows.Close()

				var totalToksAfter int64
				for rows.Next() {
					var strategy string
					var cnt, withOutbound, tokAfter, compressedTok int
					if err := rows.Scan(&strategy, &cnt, &withOutbound, &tokAfter, &compressedTok); err != nil {
						compressionStatsScanFailure("agg", err)
						continue
					}
					result.TotalRequests += cnt
					// ★ R77 轮：加的是 **withOutbound**（带 outbound body 的行数），
					//   不是整组 cnt。
					//   ★ 当前与旧写法**逐字等价**（实测窗口内 with_outbound == cnt），
					//   ★ 这是**结构性**修正：组内一旦出现混合，旧写法会把整组高估成已压缩。
					result.CompressedTotal += withOutbound
					result.StrategyDistribution[strategy] = cnt
					totalToksAfter += int64(tokAfter)
				}
				if err := rows.Err(); err != nil {
					return err
				}
				if result.TotalRequests > 0 {
					result.CompressionRate = float64(result.CompressedTotal) / float64(result.TotalRequests)
				}
				if totalToksAfter > 0 {
					result.TotalOutboundTokens = &totalToksAfter
				}
				return nil
			},
		},
		{
			// ★ 昂贵且可选 ⇒ 独立预算上限 ⇒ 拿不到就显式标注，不拖垮其它步骤。
			name:   "estimated-orig",
			budget: compressionStatsOptionalBudget,
			run: func(stepCtx context.Context) error {
				var estimatedOrig, summaryModeRows int64
				err := h.db.QueryRow(stepCtx, queries.estOrig, aggArgs...).
					Scan(&estimatedOrig, &summaryModeRows)
				if err != nil {
					return err
				}
				if estimatedOrig > 0 {
					result.EstimatedOrigTokens = &estimatedOrig
					if result.TotalOutboundTokens != nil && *result.TotalOutboundTokens > 0 && estimatedOrig > *result.TotalOutboundTokens {
						saved := estimatedOrig - *result.TotalOutboundTokens
						result.EstimatedTokensSaved = &saved
					}
				}
				// P2-C1: surface how many rows are digest envelopes (summary mode).
				// Omitted (omitempty) when zero, so the flag-off response payload is
				// byte-identical to the pre-P2-C1 shape.
				if summaryModeRows > 0 {
					result.SummaryModeRows = &summaryModeRows
				}
				return nil
			},
		},
	}

}
