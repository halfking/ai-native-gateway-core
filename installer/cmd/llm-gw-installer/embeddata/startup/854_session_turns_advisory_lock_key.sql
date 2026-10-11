-- ===========================================================================
-- File:          sql/migrations/startup/854_session_turns_advisory_lock_key.sql
-- Migration:     854
-- Database:      llm_gateway
-- Purpose:       session_turns_advisory_lock_key(TEXT, TEXT) 函数收编——
--                526 定义、mirror 血统库缺失的会话锁键函数
--
-- Status:        active
-- Idempotent:    YES (CREATE OR REPLACE FUNCTION，重复应用零行为变化)
-- Dependencies:  526_session_turns_hot.sql（原始创建者，对 mirror 血统库
--                不可整体登记：其 session_turns_with_current_month 视图
--                定义早于已应用形状，CREATE OR REPLACE VIEW 不能删列
--                ——与 523 不可整体登记、由 804 收编列集同一理由）
--
-- Background:
--   bg 的 sessionv2mirror 影写路径按名字调用
--   public.session_turns_advisory_lock_key(tenant, session)（526 定义，
--   会话轮次写方/富化/聚合/hot 行晋升共享的咨询锁键）。deploy 链（V359
--   血统）的库按历史顺序跑过 526 不受影响；mirror 血统的 host-first 库
--   表都在（session_turns / session_turns_hot 由镜像带来）、函数却缺，
--   每次影写即 42883：function public.session_turns_advisory_lock_key
--   (unknown, unknown) does not exist（2026-10-12 本机实测，
--   sessionv2mirror: V2 shadow write failed）。
--
--   函数体与 526 逐字一致（hashtextextended(tenant||':'||session, 0)，
--   IMMUTABLE/STRICT/PARALLEL SAFE）；存量库零行为变化。
-- ===========================================================================

BEGIN;

CREATE OR REPLACE FUNCTION public.session_turns_advisory_lock_key(
    p_tenant_id TEXT,
    p_session_id TEXT
)
RETURNS BIGINT
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT hashtextextended(p_tenant_id || ':' || p_session_id, 0)
$$;

COMMENT ON FUNCTION public.session_turns_advisory_lock_key(TEXT, TEXT) IS
    '854 (526 收编): canonical tenant/session advisory-lock key shared by session turn writers, enrichment, aggregation, and hot-row promotion.';

COMMIT;

-- POST_CONDITION: SELECT 1 FROM pg_proc WHERE proname = 'session_turns_advisory_lock_key'
