package admin

import (
	"github.com/kaixuan/llm-gateway-go/db"
)

// tenantStatsLogsTableSQL builds the two-arm UNION that feeds getTenantStats
// credits / by-model / by-application / daily aggregates.
//
// 2026-10-20（迁移 890，v1 族整体退役）：原来的**四臂**收敛成**两臂**。
//
//	原四臂：会话 hot/父表（冻结点之上） + v1 hot/父表（全量）
//	现两臂：会话 hot/父表
//
// 三处变化，逐一说明：
//
//  1. v1 两臂删除 —— 那两张表已不存在，留着就是 42P01。
//  2. 会话两臂对 v1 的 NOT EXISTS 双子反连接删除 —— 反连接的对象已随 v1
//     退役，留着同样是 42P01；且两条会话腿之间本来就不重叠（promote 把
//     hot 搬进母表、hot 被排空），不需要互去重。
//  3. 冻结点守卫（db.V1FreezeCutoffSQL）保留在会话两臂上：它现在是常数
//     '-infinity'（见 db/v1_freeze_cutoff.go 的 890 注释），谓词恒真 ⇒ 语义
//     等价于「会话族全量」，与删掉 v1 臂之后的事实一致。保留它是为了不改
//     调用点的语句形状。
//
// writeEnabled 现在是**历史参数**：它原来控制「会话腿要不要反连接 v1 双子」，
// 而反连接对象已随 890 退役 ⇒ 两种取值产出同一条语句。签名保留是因为调用点
// admin/tenants.go 不在本次改动的所有权范围内；调用点可以在下一次清理里
// 把它去掉。
func tenantStatsLogsTableSQL(writeEnabled bool) string {
	_ = writeEnabled
	return `(SELECT t.tenant_id::text AS tenant_id, t.ts,
	                       (CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END) AS application_id,
	                       t.model AS outbound_model, NULL::text AS client_model,
	                       t.success,
	                       NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0) AS total_tokens,
	                       t.prompt_tokens, t.completion_tokens,
	                       t.credits_charged, t.cost_usd::numeric(14,8) AS cost_usd, t.latency_ms,
	                       t.cache_read_tokens, t.cache_write_tokens
	                FROM session_turns_hot t
	                WHERE t.ts > ` + db.V1FreezeCutoffSQL + `
	        UNION ALL
	               SELECT t.tenant_id::text, t.ts,
	                      (CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END),
	                      t.model, NULL::text,
	                      t.success,
	                      NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0),
	                      t.prompt_tokens, t.completion_tokens,
	                      t.credits_charged, t.cost_usd::numeric(14,8), t.latency_ms,
	                      t.cache_read_tokens, t.cache_write_tokens
	               FROM session_turns t
	               WHERE t.ts > ` + db.V1FreezeCutoffSQL + `)`
}
