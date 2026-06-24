-- 049_armor_judgments.sql — Armor 判定审计表 (B1-1)
--
-- 记录每一次 armor judge / pattern match 的判定结果。用于:
--   1. v1 可观测模式的审计 (observe-only, 不阻断)
--   2. 召回率/精确率统计 (B1-5 测试集评估)
--   3. 误报分析 (运营调优 threshold / patterns)
--
-- 写入方: relay handler 集成 armor 后 (B1-4), 每次 Judge 调用或 pattern
-- 命中都写一行。读取方: Admin API /api/admin/armor/judgments (B1 后续)。
--
-- RLS: enabled, tenant_id 隔离 (同 assets 表约定, 用 get_current_tenant)。
-- 租户管理员只能看本租户的判定; super_admin 可跨租户 (通过 FORCE 策略,
-- 此处 v1 先不加 FORCE, Q4 按需补)。
--
-- Idempotent: safe to re-run (DROP ... IF EXISTS guards).

BEGIN;

CREATE TABLE IF NOT EXISTS public.armor_judgments (
    id              bigserial    PRIMARY KEY,
    request_id      text         NOT NULL,
    tenant_id       text         NOT NULL,
    check_type      text         NOT NULL,   -- 'prompt_inject' | 'pii' | 'hallucination' (armor.AllChecks)
    decision        text         NOT NULL,   -- 'safe' | 'warn' | 'block' (armor.Decision.String())
    source          text         NOT NULL,   -- 'pattern' | 'judge'  (哪一层判定命中)
    pattern_ids     text[]       ,            -- source=pattern 时命中的 Pattern.ID 列表
    judge_model     text         ,            -- source=judge 时使用的 LLM 模型
    score           real         ,            -- source=judge 时的 [0,1] 分数; pattern 层 NULL
    threshold       real         ,            -- 命中时的阈值 (per-tenant policy)
    mode            text         NOT NULL DEFAULT 'observe',  -- armor.Mode (v1 强制 observe)
    latency_ms      integer      NOT NULL DEFAULT 0,
    prompt_sha256   text         ,            -- prompt 的 SHA256 (隐私: 不存原文, 可去重统计)
    snippet         text         ,            -- 命中片段 (≤80 字符, patterns.go snippetAround)
    reason          text         ,            -- judge 给的可读解释
    created_at      timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT chk_armor_decision CHECK (decision IN ('safe', 'warn', 'block')),
    CONSTRAINT chk_armor_source  CHECK (source IN ('pattern', 'judge')),
    CONSTRAINT chk_armor_mode    CHECK (mode IN ('observe', 'enforce')),
    CONSTRAINT chk_armor_check   CHECK (check_type IN ('prompt_inject', 'pii', 'hallucination'))
);

-- 热查询: 按租户 + 时间倒序 (Admin 列表)
CREATE INDEX IF NOT EXISTS idx_armor_judgments_tenant_time
    ON public.armor_judgments (tenant_id, created_at DESC);

-- 按请求查 (一次请求可能多次判定: pattern + judge)
CREATE INDEX IF NOT EXISTS idx_armor_judgments_request
    ON public.armor_judgments (request_id);

-- 按判定类型 + decision 聚合 (B1-5 召回率/精确率统计)
CREATE INDEX IF NOT EXISTS idx_armor_judgments_stats
    ON public.armor_judgments (check_type, decision);

-- 自动分区按月 (复用 request_logs 模式, 避免无限增长)
-- 注: 如需 hypertable, 部署时执行 SELECT create_hypertable('armor_judgments','created_at')
--     (本 migration 保持纯 SQL, hypertable 转换在 init-scripts 单独跑)

-- RLS: 租户隔离
ALTER TABLE public.armor_judgments ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_armor_judgments ON public.armor_judgments;
CREATE POLICY tenant_isolation_armor_judgments ON public.armor_judgments
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

COMMIT;
