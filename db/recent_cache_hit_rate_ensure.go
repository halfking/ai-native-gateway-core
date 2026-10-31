package db

import (
	"context"
	"log/slog"
)

// recent_cache_hit_rate_ensure.go —— P1-B-③（迁移 915）的运行时 SSOT 与
// boot 自愈，2026-10-28 211 routing_schema_error 事故轮补上的防线。
//
// # 为什么必须有 boot 自愈（当晚事故的结构性教训）
//
// provider/client.go 的候选查询以 CROSS JOIN LATERAL 调用本函数 ⇒ 函数缺席
// ⇒ 整条候选查询 42883 ⇒ 选不出任何上游 ⇒ 100% 流量失败。而「二进制先上、
// 迁移后到」的窗口在部署序列断链时会真实出现（当晚 211：files=() 漏登记
// 915 ⇒ 函数从未建 ⇒ 每个 /v1/chat/completions 500）。
//
// recent_success_rate 早有同款防线（ensureRoutingRecentSuccessRate：每次
// boot CREATE OR REPLACE 收敛函数体）⇒ 同类事故对 rsr 不可能复发。
// 本文件把 recent_cache_hit_rate 对齐到同一防线：boot 时无条件收敛到
// recentCacheHitRateEnsureSQL 的形态——迁移通道断链时二进制自己补函数，
// 函数体被带外改坏时下一次重启自愈。
//
// # 与迁移 915 的等价性
//
// 迁移文件（sql/migrations/startup/915_recent_cache_hit_rate.sql）与这里的
// SSOT 是同一函数体的两份载体，逐文本等价由
// sql/migrations/startup/migration_915_recent_cache_hit_rate_test.go 钉住
// （890 对 recent_success_rate 的同款契约门）。两者漂移的后果不是编译错，
// 而是「迁移装一个体、重启换另一个体」——所以漂移必须静态层拦住。
//
// # 前置与失败语义
//
// 函数体读 public.session_turns_hot（LANGUAGE sql 在 CREATE 时校验表引用）
// ⇒ 本 ensure 必须排在会话族就位之后；ensureSchema 链上紧跟
// ensureRoutingRecentSuccessRate（rsr 同读该表，二者前置完全一致）。
// 失败语义与 rsr 相同：响亮失败（return err ⇒ boot 中止），绝不静默降级——
// 函数缺席时网关「能起但每个请求都 500」，比起不来更难发现。
func (d *DB) ensureRecentCacheHitRate(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}
	if _, err := d.pool.Exec(ctx, recentCacheHitRateEnsureSQL); err != nil {
		return err
	}
	slog.Info("routing recent cache-hit-rate schema ensured (recent_cache_hit_rate fn)")
	return nil
}

// recentCacheHitRateEnsureSQL 是 recent_cache_hit_rate 的运行时 SSOT。
//
// 体读会话族热表 session_turns_hot（890 之后业务路由行唯一落点）：
//   - credential_id 是 TEXT（707 S1a）⇒ 数字守卫 CAST 后比较；
//   - 模型键 model/canonical_model 回退链（旧 request_logs 的
//     outbound_model/client_model 列名在这里不存在）；
//   - 探针/自检排除与 recent_success_rate 逐字一致；
//   - rate = 逐行 clamp 后的 token 加权 cache_read/prompt；分母 NULLIF
//     兜底（谓词已保证 prompt_tokens > 0，双保险）；空窗 ⇒ rate NULL +
//     samples 0 ⇒ 调用方按「无信号 → 中性」fail-open。
//
// ⚠ 注释里不许出现反引号：本段 SQL 在 Go raw string 里。
const recentCacheHitRateEnsureSQL = `
		CREATE OR REPLACE FUNCTION public.recent_cache_hit_rate(
		    p_credential_id bigint,
		    p_raw_model text,
		    p_sample_n integer DEFAULT 50,
		    p_window_hours integer DEFAULT 3
		) RETURNS TABLE(rate double precision, samples integer)
		LANGUAGE sql
		STABLE
		AS $$
		    WITH recent AS (
		        SELECT prompt_tokens, cache_read_tokens, ts
		        FROM public.session_turns_hot
		        WHERE credential_id ~ '^[0-9]+$'
		          AND credential_id::bigint = p_credential_id
		          AND lower(COALESCE(model, canonical_model)) = lower(p_raw_model)
		          AND ts > NOW() - (p_window_hours || ' hours')::interval
		          AND COALESCE(task_type, '') <> 'probe_triggered'
		          AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
		          AND request_id NOT LIKE 'probe-%'
		          AND prompt_tokens IS NOT NULL AND prompt_tokens > 0
		        ORDER BY ts DESC
		        LIMIT p_sample_n
		    )
		    SELECT (SUM(LEAST(GREATEST(COALESCE(cache_read_tokens, 0), 0), prompt_tokens))::double precision
		            / NULLIF(SUM(prompt_tokens), 0))::double precision,
		           COUNT(*)::int
		    FROM recent;
		$$;
	`
