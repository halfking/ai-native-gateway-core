-- Migration 513 DOWN: revert V3.2 schema unification + session_turns dual-write
-- ─────────────────────────────────────────────────────────────────────────────
-- 警告: 这是高风险操作。如果生产数据已经写到 public.session_turns 的 T0..T9 列，
--      应该先备份再 DROP COLUMN；如果 gateway schema 是干净的（430 跑过但 V2 未启用），按本文件反向操作即可。
--
-- 注: 历史上 migration 430 在 public schema 已经创建了 session_turns 等 4 张表。
--     本 down 脚本不会去删 public.xx 表（不在事务范围内），只：
--       1. 重建 gateway schema
--       2. DROP public.session_turns 新加的 10 列 + 索引
--       3. 恢复 cleanup_expired_session_turn_logs() 函数到 gateway 引用
--         （这是错的，但 down 必须撤销本 up 的所有改动才能整体回到 up 之前）

BEGIN;

-- 1. DROP public.session_turns 新加的 10 列 + 索引
DROP INDEX IF EXISTS public.idx_session_turns_t0_arrived;

ALTER TABLE public.session_turns
    DROP COLUMN IF EXISTS t9_response_end_at,
    DROP COLUMN IF EXISTS t8_response_start_at,
    DROP COLUMN IF EXISTS t7_forward_start_at,
    DROP COLUMN IF EXISTS t6_cred_dequeued_at,
    DROP COLUMN IF EXISTS t5_cred_enqueued_at,
    DROP COLUMN IF EXISTS t4_model_dequeued_at,
    DROP COLUMN IF EXISTS t3_model_enqueued_at,
    DROP COLUMN IF EXISTS t2_total_dequeued_at,
    DROP COLUMN IF EXISTS t1_total_enqueued_at,
    DROP COLUMN IF EXISTS t0_arrived_at;

-- 2. 恢复 cleanup_expired_session_turn_logs() 函数 → gateway schema 引用
--    (这是 down 把指针改回错误的方向,但 down 必须撤销 up 改动;
--     真正的修复在 up 中已经做了,operator 重新 up 即可)
CREATE OR REPLACE FUNCTION public.cleanup_expired_session_turn_logs() RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    deleted_count INT;
BEGIN
    DELETE FROM public.session_turn_logs
    WHERE expires_at < NOW();

    GET DIAGNOSTICS deleted_count = ROW_COUNT;

    IF deleted_count > 0 THEN
        RAISE NOTICE 'Cleaned up % expired session turn logs', deleted_count;
    END IF;
END;
$$;

-- 3. 重建 gateway schema + 4 张表（必须 IF NOT EXISTS，
--    避免 430 重新部署时 NoOp 数据被误删）
CREATE SCHEMA IF NOT EXISTS gateway;

CREATE TABLE IF NOT EXISTS public.sessions (
    id BIGSERIAL,
    tenant_id VARCHAR(255) NOT NULL,
    session_id TEXT NOT NULL,
    turn_count INT NOT NULL DEFAULT 0,
    message_count INT NOT NULL DEFAULT 0,
    first_user_ts TIMESTAMPTZ,
    last_assistant_ts TIMESTAMPTZ,
    title TEXT,
    summary TEXT,
    user_tags JSONB DEFAULT '{}'::jsonb,
    project_id TEXT,
    task_id TEXT,
    outcome TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    PRIMARY KEY (id, partition_date),
    UNIQUE (tenant_id, session_id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE TABLE IF NOT EXISTS public.session_turns (
    id BIGSERIAL,
    session_id TEXT NOT NULL,
    turn_no INT NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    request_id TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submit_mode TEXT NOT NULL DEFAULT 'full',
    compression_applied BOOLEAN DEFAULT FALSE,
    compression_strategy TEXT,
    compression_meta JSONB DEFAULT '{}'::jsonb,
    compression_tokens_saved INT,
    injection_verdict TEXT DEFAULT 'skip',
    output_verdict TEXT DEFAULT 'skip',
    model TEXT,
    provider TEXT,
    credential_id TEXT,
    prompt_tokens INT,
    completion_tokens INT,
    cache_read_tokens INT,
    cache_write_tokens INT,
    cost_usd NUMERIC(12,6),
    latency_ms INT,
    status_code INT,
    success BOOLEAN,
    error_kind TEXT,
    source_kind TEXT NOT NULL DEFAULT 'live',
    quality TEXT NOT NULL DEFAULT 'verified',
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    PRIMARY KEY (id, partition_date),
    UNIQUE (tenant_id, session_id, turn_no, partition_date),
    UNIQUE (tenant_id, request_id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE TABLE IF NOT EXISTS public.session_bodies (
    id BIGSERIAL,
    tenant_id VARCHAR(255) NOT NULL,
    session_id TEXT NOT NULL,
    turn_no INT NOT NULL,
    request_id TEXT NOT NULL,
    request_delta JSONB,
    response_delta JSONB,
    outbound_body JSONB,
    request_attachments JSONB DEFAULT '[]'::jsonb,
    response_attachments JSONB DEFAULT '[]'::jsonb,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    partition_date DATE NOT NULL DEFAULT CURRENT_DATE,
    PRIMARY KEY (id, partition_date),
    UNIQUE (tenant_id, request_id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE TABLE IF NOT EXISTS public.session_turn_logs (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(255) NOT NULL,
    session_id TEXT NOT NULL,
    turn_no INT NOT NULL,
    request_id TEXT NOT NULL,
    stage TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    latency_ms INT,
    event_data JSONB DEFAULT '{}'::jsonb,
    error_msg TEXT,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '24 hours')
);

CREATE INDEX IF NOT EXISTS idx_session_turn_logs_expires
    ON public.session_turn_logs (expires_at);

-- 4. 确保 gateway schema 下的 RLS / 策略也还原（按 430 设定）。

COMMIT;

-- POST_CONDITION (manual gate after down):
--   1. \dt gateway.*              → 4 张表回来
--   2. SELECT column_name FROM information_schema.columns
--      WHERE table_schema = 'public' AND table_name = 'session_turns'
--        AND column_name ~ '^t[0-9]_.+_at$'
--      → 0 行（10 列已 DROP）
--   3. 部署失败回滚警告:本次 down 调用后 Go 代码里如果有 gateway.* 引用需要
--      手工还原。V3.2 后端闭包不会触发这个 down,记录到 incident log。
