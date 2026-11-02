-- 928: 探测流量物理隔离车道 —— 探测流水 / 分钟计数与业务表分离。
--
-- 方案：docs/03-design/04-data-design/probe-traffic-physical-isolation-plan-2026-11-02.md
--
-- # 本迁移只建表（写路径由 LLM_GATEWAY_PROBE_LANE_PHYSICAL=1 打开）
--
--   ① session_turns_probe_hot —— 探测请求流水热表（窄列；TTL 由后续清理作业管）
--   ② request_stats_probe_minute —— 探测分钟聚合（形状对齐 request_stats_minute）
--
-- ★ 不建全量 session_turns 分区镜像：探测流水以热表 + 短 TTL 为终态足够；
-- ★ 避免把 100+ 列业务表债务复制进探测族。
--
-- 可重入：CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE IF NOT EXISTS public.session_turns_probe_hot (
    request_id         text PRIMARY KEY,
    ts                 timestamptz NOT NULL DEFAULT now(),
    tenant_id          text NOT NULL DEFAULT 'default',
    credential_id      text,
    raw_model_name     text,
    client_model       text,
    outbound_model     text,
    success            boolean,
    status_code        integer,
    error_kind         text,
    latency_ms         integer,
    prompt_tokens      integer,
    completion_tokens  integer,
    total_tokens       integer,
    origin_stage       text,
    origin_actor       text,
    task_type          text,
    api_key_id         text,
    provider_id        bigint,
    client_ip          text,
    request_preview    text,
    gw_session_id      text,
    agent_name         text,
    quality_flags      text[],
    created_at         timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.session_turns_probe_hot IS
  'Probe-lane request ledger (physical isolation from session_turns_hot). Writers gated by LLM_GATEWAY_PROBE_LANE_PHYSICAL.';

CREATE INDEX IF NOT EXISTS idx_session_turns_probe_hot_ts
  ON public.session_turns_probe_hot (ts DESC);

CREATE INDEX IF NOT EXISTS idx_session_turns_probe_hot_stage_ts
  ON public.session_turns_probe_hot (origin_stage, ts DESC);

CREATE INDEX IF NOT EXISTS idx_session_turns_probe_hot_tenant_ts
  ON public.session_turns_probe_hot (tenant_id, ts DESC);

CREATE TABLE IF NOT EXISTS public.request_stats_probe_minute (
    bucket             timestamptz NOT NULL,
    tenant_id          text DEFAULT 'default'::text NOT NULL,
    provider_id        bigint DEFAULT 0 NOT NULL,
    canonical_id       bigint DEFAULT 0 NOT NULL,
    requests           bigint DEFAULT 0 NOT NULL,
    success_count      bigint DEFAULT 0 NOT NULL,
    failure_count      bigint DEFAULT 0 NOT NULL,
    prompt_tokens      bigint DEFAULT 0 NOT NULL,
    completion_tokens  bigint DEFAULT 0 NOT NULL,
    total_tokens       bigint DEFAULT 0 NOT NULL,
    credits_charged    bigint DEFAULT 0 NOT NULL,
    cost_usd           numeric(18,8) DEFAULT 0 NOT NULL,
    latency_ms_sum     bigint DEFAULT 0 NOT NULL,
    PRIMARY KEY (bucket, tenant_id, provider_id, canonical_id)
);

COMMENT ON TABLE public.request_stats_probe_minute IS
  'Per-minute probe-lane usage aggregates. Parallel to request_stats_minute; never mixed into business board KPIs.';

COMMIT;
