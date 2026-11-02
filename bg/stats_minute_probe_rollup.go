package bg

// stats_minute_probe_rollup.go —— 探测分钟聚合的**真实写入路径**（2026-11-02）。
//
// # 为什么这个文件存在
//
// 探测物理隔离车道开启（LLM_GATEWAY_PROBE_LANE_PHYSICAL=1）后，探测流量
// 不进业务表（usage_ledger_hot / session_turns_hot / request_logs 视图），
// 改写 session_turns_probe_hot（迁移 928 建）。
//
// ★ 审计发现（2026-11-02）：探测分钟聚合原先只挂在
//   domains/stats.MinuteAccumulator.Record 上，而 **NewMinuteAccumulator
//   全仓无调用方**（cmd/gateway/main.go:273 的 statsMinuteAccumulator 是个
//   从未赋值的 nil 变量）⇒ Record 永不执行 ⇒ request_stats_probe_minute
//   **生产恒空** ⇒ 读侧端点 /api/admin/probe/lane/stats 永远返回 0。
//
// ★ 业务分钟表的**真实**写入者是本文件所在的 rollup：
//   bg.StatsMinuteRollup（cmd/gateway/main.go:5667 实例化并 Start）——
//   它每分钟扫 request_logs_with_current_month 视图整桶替换业务表。
//   视图里没有探测行（隔离开启后探测不进业务表）⇒ 业务表天然不含探测；
//   但也意味着**没有任何东西**会写探测分钟表。
//
// ⇒ 本文件补上探测臂：从 session_turns_probe_hot 汇总进
//   request_stats_probe_minute，与业务臂同形状、同键、同替换语义。
//
// # 与业务臂的差异（每一处都是刻意的，不是遗漏）
//
//  1. **credits_charged / cost_usd 恒为 0**：探测**不计费**（方案 §2 第 3 条
//     「探测不计费、不抬 Key 计数」）。业务臂用 maas.RequestLogCreditsSQL
//     从 model_suppliers 定价表算 credits，探测族表没有 pricing 关联列，
//     也不该算——探测消耗的是真实上游额度，但**不应计入用户账单**。
//     ⇒ 探测分钟表的这两列是 0，读它的看板不能拿它算钱。
//
//  2. **canonical_id 恒为 0**：探测族表没有 canonical_id 列（窄表设计）。
//     ⇒ 探测分钟表的桶粒度退化为 (bucket, tenant_id, provider_id, 0)。
//     这与业务表的 (bucket, tenant_id, provider_id, canonical_id) 不同粒度，
//     但两张表**不共享键空间**（不同表名）⇒ 不会互相覆盖。
//
//  3. **request_status 不存在**：探测族表存的是 success / status_code。
//     ⇒ 成功计数取 success=true，失败计数取 success=false。
//     ⚠ 与业务臂的差异：业务臂的 request_status 枚举含 rate_limited，
//     探测臂把它并入失败（探测不会被限流计价，语义上算失败更贴近事实）。
//
//  4. **整桶替换（= EXCLUDED），不累加**：与业务臂 rollupMainStatement
//     的 ON CONFLICT DO UPDATE SET x = EXCLUDED.x **逐字同构**。
//     ⇒ 同一分钟被重复 rollup（重试 / RebuildRangeStats）不会重复计数。
//     ★ 这与 domains/stats/minute_flush.go 的 upsertProbeMinuteRow
//       （累加式 + EXCLUDED）**刻意不同**：后者挂在死代码上；
//       本文件是生产路径 ⇒ 必须用可重入的替换语义。
//
// # 开关
//
// ★ 只在物理车道开启时才写探测分钟表：车道关闭时探测流量在业务表里，
//   由业务臂的排除谓词处理（bg/probe_policy.go 的
//   ProbeTrafficExclusionPredicateView）—— 那时探测表是空的，本臂也
//   应当不写（否则会出现「同一批探测流量既被业务臂排除、又被探测臂统计」
//   的重复口径）。

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

// rollupProbeStatement materialises request_stats_probe_minute from
// session_turns_probe_hot.
//
// ★ 整桶替换：ON CONFLICT DO UPDATE SET x = EXCLUDED.x（与业务臂同构）。
// ★ 只在车道开启时执行（调用方判断），避免与业务臂的探测排除谓词重复口径。
const rollupProbeStatement = `
	INSERT INTO request_stats_probe_minute (
		bucket, tenant_id, provider_id, canonical_id,
		requests, success_count, failure_count,
		prompt_tokens, completion_tokens, total_tokens,
		credits_charged, cost_usd, latency_ms_sum
	)
	SELECT
		date_trunc('minute', r.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
		COALESCE(NULLIF(r.tenant_id, ''), 'default'),
		COALESCE(r.provider_id, 0),
		0,
		COUNT(*)::bigint,
		COUNT(*) FILTER (WHERE r.success)::bigint,
		COUNT(*) FILTER (WHERE NOT r.success)::bigint,
		COALESCE(SUM(r.prompt_tokens), 0)::bigint,
		COALESCE(SUM(r.completion_tokens), 0)::bigint,
		COALESCE(SUM(COALESCE(r.prompt_tokens,0)+COALESCE(r.completion_tokens,0)), 0)::bigint,
		0::bigint,
		0::numeric,
		COALESCE(SUM(r.latency_ms), 0)::bigint
	FROM session_turns_probe_hot r
	WHERE r.ts >= $1 AND r.ts <= $2
	  AND ($3 = '' OR r.tenant_id = $3)
	GROUP BY 1, 2, 3
	ON CONFLICT (bucket, tenant_id, provider_id, canonical_id) DO UPDATE SET
		requests = EXCLUDED.requests,
		success_count = EXCLUDED.success_count,
		failure_count = EXCLUDED.failure_count,
		prompt_tokens = EXCLUDED.prompt_tokens,
		completion_tokens = EXCLUDED.completion_tokens,
		total_tokens = EXCLUDED.total_tokens,
		credits_charged = EXCLUDED.credits_charged,
		cost_usd = EXCLUDED.cost_usd,
		latency_ms_sum = EXCLUDED.latency_ms_sum
`

// rollupProbe aggregates the probe lane into request_stats_probe_minute.
//
// It is a no-op when the physical lane is off: with the lane off, probe
// traffic still lives in the business tables and is handled by the
// business arm's exclusion predicate — writing it here as well would
// count the same traffic twice under two different definitions.
//
// ★ 错误不吞：返回给调用方，让 rollupWindow 的失败语义（游标不前进 ⇒
//
//	重试收敛）与业务臂保持一致。
func (w *StatsMinuteRollup) rollupProbe(ctx context.Context, since, until time.Time) error {
	if w == nil || w.db == nil {
		return nil
	}
	if !probelane.PhysicalEnabled() {
		return nil
	}
	_, err := w.db.Exec(ctx, rollupProbeStatement, since, until, "")
	if err != nil {
		return err
	}
	return nil
}

// retireProbeMinutes deletes probe minute rows that the current window no
// longer produces.
//
// ★ 为什么业务臂有 retire、探测臂也要有：rollup 是**整桶替换**，但只替换
//
//	**本次窗口**覆盖到的键。窗口推进后，早期那些「曾经有探测流量、现在
//	没有」的桶会**永远留在表里**（业务臂的 retireClosedDims 就是干这个的，
//	见 stats_minute_rollup_retire.go）。
//	⇒ 不做 retire ⇒ 探测分钟表会累积**已消失的探测桶**，读侧算出的
//	「探测 QPS」会虚高。
//
// ★ 保守实现：只删「窗口之前、且窗口内没有任何探测流量」的桶。
//
//	用 NOT EXISTS 判「同键在窗口内仍有行」比逐键比对便宜，且天然幂等。
const retireProbeMinutesStatement = `
	DELETE FROM request_stats_probe_minute m
	WHERE m.bucket < $1
	  AND NOT EXISTS (
		SELECT 1 FROM session_turns_probe_hot r
		 WHERE date_trunc('minute', r.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' = m.bucket
		   AND r.tenant_id = m.tenant_id
		   AND COALESCE(r.provider_id, 0) = m.provider_id
	  )
`

// retireProbeMinutes removes probe minute buckets that no longer have
// underlying probe rows.
//
// ★ 边界声明：本函数只处理「桶已无源数据」的退役。
//
//	源流水 / 分钟表本身的 TTL 由 PartitionManager.cleanupOldProbeLaneTables
//	承担（U1，lifecycle.session_turns_probe_hot_ttl_days，默认 14d）。
//	⇒ 探测流量停止后，retire 让无源桶消失；过期源行由 TTL 作业删除。
func (w *StatsMinuteRollup) retireProbeMinutes(ctx context.Context, since time.Time) error {
	if w == nil || w.db == nil {
		return nil
	}
	if !probelane.PhysicalEnabled() {
		return nil
	}
	if _, err := w.db.Exec(ctx, retireProbeMinutesStatement, since); err != nil {
		return err
	}
	return nil
}

// logProbeRollupError logs a probe rollup failure without aborting the
// business arm.
//
// ★ 为什么探测臂失败不阻断业务臂：业务分钟表是**用户可见的 KPI**，
//
//	探测分钟表是**运维可见的辅助面**。让一个辅助面的写入失败拖垮业务
//	KPI 的刷新，是把可选性的东西变成必需性的东西。
//	⇒ 但必须**响亮**：只吞不响 = 下次读侧看到 0 行时无人知道原因。
func logProbeRollupError(op string, err error) {
	slog.Error("stats probe rollup failed", "op", op, "error", err)
}
