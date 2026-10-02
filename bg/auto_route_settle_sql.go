package bg

import (
	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// settleBatch 与 loadTaskBaselines 的 SQL 构造（审计 §9.44）。
//
// # 为什么要抽出来
//
// §9.43 把三条腿接到 `src.TurnsTable` / `src.SessionKeyCol`，靠 `currentSettleSource()`
// 读全局 S4 写门。**这使会话族分支无法被测试**：
// `settings.RequestLogsWriteEnabled()` 只有读取器（`GetPlatformBool` 覆盖 settings
// store），没有能在测试里翻转的注入口 ⇒ 集成测试只能跑 v1 分支，而 v1 分支本来
// 就是今天线上跑的那条。§9.43 交付时会话族 SQL **一次都没被执行过**。
//
// 抽成「接收 src、返回 SQL」的纯函数后，测试可以直接用 `settleSourceFor(false)`
// 拿到会话族规格并把**同一段文本**发给真实 PostgreSQL。这是本文件存在的唯一理由：
// 让「源切换后行为不变」这句话有一处可被执行的定义，而不是两段各自正确的字符串。
//
// # 不变量
//
// 这两个函数是 worker 实际发送的那段文本的**唯一定义**。任何想验证 SQL 形状的
// 测试都必须调用它们，不得在测试里另抄一份——抄一份就变成「测试守住的是副本」。
// TestSettleSQLBuildersAreTheOnlyDefinitionOfTheLegs 钉住这一点。

// settleBaselinesSQL 构造 cohort p95/p75 的基线查询。
//
// 表名与会话键列名来自 src，其余谓词两族逐字相同：基线的**总体定义**（近
// baselineWindow 的 is_auto_request 行、排除合成 actor、latency 非空）不随源族改变，
// 改变的只是这些行从哪张表来。
func settleBaselinesSQL(src settleSourceSpec) string {
	return `
		SELECT COALESCE(task_type, '') AS task_type,
		       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::int AS p95_latency_ms,
		       COALESCE(percentile_cont(0.75) WITHIN GROUP (ORDER BY cost_usd), 0)        AS p75_cost_usd,
		       count(*)                                                                     AS cohort_rows
		FROM ` + src.TurnsTable + ` rl
		WHERE rl.ts >= NOW() - $1::interval
		  AND rl.is_auto_request = TRUE
		  AND rl.latency_ms IS NOT NULL` + autoroute.SQLExcludeSyntheticActors("rl") + `
		GROUP BY task_type
	`
}

// settlePendingSQL 构造取待结算 selection + 其 outcome + 会话归因的查询。
//
// $1 = settleDelay，$2 = settleBatchSize。
//
// 三个读点共用 src：
//   - outcome join：rl.request_id = s.request_id
//   - 会话摘要：ss.session_key = s.session_id（session_summaries 不随源族改变）
//   - LATERAL：按 src.SessionKeyCol 聚合同会话的行，得 model_reqs / retry_count
//
// LATERAL 的 `retryCountPerRowSQL` 分派逻辑见该函数注释（§9.41：`routing_attempts`
// 是 object 不是 array，旧表达式对真实数据必然抛错并中止整条语句）。
func settlePendingSQL(src settleSourceSpec) string {
	return `
			SELECT s.id, s.partition_date, s.request_id, s.task_type, s.canonical_id, s.ts,
			       rl.success, rl.latency_ms, rl.cost_usd,
			       rl.origin_actor,
			       rl.canonical_id        AS rl_canonical_id,
			       LEFT(rl.tenant_id, 64) AS rl_tenant_id,
			       ss.health_score, ss.error_count, ss.request_count,
			       mr.model_reqs, mr.retry_count
			FROM (
				SELECT id, partition_date, request_id, task_type, canonical_id, ts, session_id
				FROM auto_route_selections_hot
				WHERE settled_at IS NULL AND ts < NOW() - $1::interval
				ORDER BY ts
				LIMIT $2
			) s
			LEFT JOIN ` + src.TurnsTable + ` rl
			       ON rl.request_id = s.request_id
			LEFT JOIN session_summaries ss
			       ON ss.session_key = s.session_id
			LEFT JOIN LATERAL (
			       SELECT COUNT(*)::int AS model_reqs,
			              SUM(` + retryCountPerRowSQL("r2") + `)::int AS retry_count
			       FROM ` + src.TurnsTable + ` r2
			       WHERE s.session_id IS NOT NULL
			         AND r2.` + src.SessionKeyCol + ` = s.session_id` + autoroute.SQLExcludeSyntheticActors("r2") + `
			) mr ON TRUE
	`
}
