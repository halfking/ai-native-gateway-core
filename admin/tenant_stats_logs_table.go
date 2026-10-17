package admin

import (
	"github.com/kaixuan/llm-gateway-go/db"
)

// tenantStatsLogsTableSQL builds the four-arm UNION that feeds getTenantStats
// credits / by-model / by-application / daily aggregates.
//
// writeEnabled mirrors settings.RequestLogsWriteEnabled():
//
//   - true（双写期 / 回滚）：会话腿带冻结点 + NOT EXISTS v1 双子排除（S3 波 3
//     定稿）。少了反连接会在翻转边界双计（镜像 ts ≠ v1 ts，实测 1 例）。
//   - false（S4 停写）：会话腿只保留冻结点守卫，**撤掉**对 _request_logs
//     家族的相关子查询反连接。停写后 v1 不再进新行，反连接对空父表做
//     逐行分区探测（default 租户 1 天窗实测 ~1.4s/段 × 4 段并行）把 10s
//     预算打穿成 504；撤掉后同窗 credits ~0.26s、by-app ~0.19s。冻结点
//     仍在，停写前历史继续由两条裸 v1 腿供数。
func tenantStatsLogsTableSQL(writeEnabled bool) string {
	sessionDedup := ""
	if writeEnabled {
		sessionDedup = `
	                  AND NOT EXISTS (SELECT 1 FROM _request_logs_hot h WHERE h.request_id = t.request_id)
	                  AND NOT EXISTS (SELECT 1 FROM _request_logs p WHERE p.request_id = t.request_id) -- sqlreadguard:allow S3 波3 会话补丁腿：会话腿必须反连接 v1 母表，否则 promote 前后同一 request_id 会被两腿各计一次（脚注腿）`
	}
	sessionDedupParent := sessionDedup
	if writeEnabled {
		// Parent session_turns leg mirrors the hot-leg probes; keep the
		// sqlreadguard wording that names both legs so the shape pin stays
		// meaningful when someone edits only one arm.
		sessionDedupParent = `
	                 AND NOT EXISTS (SELECT 1 FROM _request_logs_hot h WHERE h.request_id = t.request_id)
	                 AND NOT EXISTS (SELECT 1 FROM _request_logs p WHERE p.request_id = t.request_id) -- sqlreadguard:allow 同上：v1 腿与会话腿共用同一套反连接去重，否则 v1 腿行数虚高`
	}
	return `(SELECT t.tenant_id::text AS tenant_id, t.ts,
	                       (CASE WHEN t.application_id ~ '^[0-9]+$' THEN t.application_id::bigint END) AS application_id,
	                       t.model AS outbound_model, NULL::text AS client_model,
	                       t.success,
	                       NULLIF(COALESCE(t.prompt_tokens, 0) + COALESCE(t.completion_tokens, 0), 0) AS total_tokens,
	                       t.prompt_tokens, t.completion_tokens,
	                       t.credits_charged, t.cost_usd::numeric(14,8) AS cost_usd, t.latency_ms,
	                       t.cache_read_tokens, t.cache_write_tokens
	                FROM session_turns_hot t
	                WHERE t.ts > ` + db.V1FreezeCutoffSQL + sessionDedup + `
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
	               WHERE t.ts > ` + db.V1FreezeCutoffSQL + sessionDedupParent + `
	        UNION ALL
	               SELECT tenant_id, ts, application_id, outbound_model, client_model,
	                      success, total_tokens, prompt_tokens, completion_tokens,
	                      credits_charged, cost_usd, latency_ms,
	                      cache_read_tokens, cache_write_tokens
	                FROM _request_logs_hot
	        UNION ALL
	               SELECT tenant_id, ts, application_id, outbound_model, client_model,
	                      success, total_tokens, prompt_tokens, completion_tokens,
	                      credits_charged, cost_usd, latency_ms,
	                      cache_read_tokens, cache_write_tokens
	        FROM _request_logs) -- sqlreadguard:allow R36-A1 漏热尾根修的双腿之母表腿（hot 腿同查询内联；252-dev 实测 NOT EXISTS 反连接 + 租户过滤 COUNT 30s 超时，故走 hot∪母表 UNION ALL）+ S3 波 3 会话补丁腿（冻结点切分；停写路径撤双子反连接，双写路径保留）`
}
