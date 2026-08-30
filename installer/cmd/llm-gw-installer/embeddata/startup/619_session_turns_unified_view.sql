-- ===========================================================================
-- File:          sql/migrations/startup/619_session_turns_unified_view.sql
-- Database:      llm_gateway
-- Purpose:       创建 session_turns_unified 统一视图（hot + 分区表）
--
-- Related:       sql/migrations/startup/526_session_turns_hot.sql
--                sql/migrations/startup/430_sessions_v2_schema.sql
-- Status:        active
-- Idempotent:    YES
--
-- Context:
--   2026-08-29 审计发现：session_turns_hot 缺少统一视图，导致 8 小时内的
--   会话轮次数据无法被查询到。session_bodies_hot 已有 session_bodies_unified
--   视图（Migration 614），本迁移为 session_turns_hot 添加相同的模式。
--
-- Architecture:
--   - session_turns_hot: 最近 8 小时的数据，heap 表，支持 UPDATE/DELETE
--   - session_turns_YYYY_MM: 历史数据，columnar 分区表，只读
--   - session_turns_unified: UNION ALL 视图，应用层使用此视图查询
--
-- Changelog:
--   2026-08-29  v1.0  初始创建 - 统一视图
-- ===========================================================================

\set ON_ERROR_STOP on

-- 创建统一视图
-- 使用 UNION ALL 联合 hot 表和所有月度分区
-- 注意：视图中列的顺序必须与两个表的列顺序一致
-- security_invoker=true 保证调用方身份而非视图所有者执行底层查询。
CREATE OR REPLACE VIEW session_turns_unified
    WITH (security_invoker=true) AS
SELECT
    id,
    session_id,
    turn_no,
    tenant_id,
    request_id,
    project_id,
    namespace,
    parent_request_id,
    task_type,
    ts,
    submit_mode,
    compression_applied,
    compression_strategy,
    compression_meta,
    compression_tokens_saved,
    injection_verdict,
    output_verdict,
    model,
    provider,
    credential_id,
    prompt_tokens,
    completion_tokens,
    cache_read_tokens,
    cache_write_tokens,
    cost_usd,
    latency_ms,
    status_code,
    success,
    error_kind,
    source_kind,
    quality,
    partition_date,
    attachment_count,
    attachment_total_bytes,
    multimodal_types,
    attempt_no,
    tools,
    title,
    summary,
    aggregate_applied_at,
    t0_arrived_at,
    t1_total_enqueued_at,
    t2_total_dequeued_at,
    t3_model_enqueued_at,
    t4_model_dequeued_at,
    t5_cred_enqueued_at,
    t6_cred_dequeued_at,
    t7_forward_start_at,
    t8_response_start_at,
    t9_response_end_at
FROM session_turns_hot

UNION ALL

SELECT
    id,
    session_id,
    turn_no,
    tenant_id,
    request_id,
    project_id,
    namespace,
    parent_request_id,
    task_type,
    ts,
    submit_mode,
    compression_applied,
    compression_strategy,
    compression_meta,
    compression_tokens_saved,
    injection_verdict,
    output_verdict,
    model,
    provider,
    credential_id,
    prompt_tokens,
    completion_tokens,
    cache_read_tokens,
    cache_write_tokens,
    cost_usd,
    latency_ms,
    status_code,
    success,
    error_kind,
    source_kind,
    quality,
    partition_date,
    attachment_count,
    attachment_total_bytes,
    multimodal_types,
    attempt_no,
    tools,
    title,
    summary,
    aggregate_applied_at,
    t0_arrived_at,
    t1_total_enqueued_at,
    t2_total_dequeued_at,
    t3_model_enqueued_at,
    t4_model_dequeued_at,
    t5_cred_enqueued_at,
    t6_cred_dequeued_at,
    t7_forward_start_at,
    t8_response_start_at,
    t9_response_end_at
FROM session_turns;

-- 注释
COMMENT ON VIEW session_turns_unified IS 
    '统一视图：联合 session_turns_hot (最近8小时) 和 session_turns 月度分区表。应用层应使用此视图查询完整数据。';

-- 验证
DO $$
BEGIN
    ASSERT (SELECT COUNT(*) FROM pg_views 
            WHERE viewname = 'session_turns_unified') = 1,
        'View session_turns_unified not created';
    
    RAISE NOTICE '✅ Migration 617 completed: session_turns_unified view created';
END $$;
