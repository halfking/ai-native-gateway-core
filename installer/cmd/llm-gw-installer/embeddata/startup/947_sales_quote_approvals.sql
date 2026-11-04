-- ===========================================================================
-- File:          sql/migrations/startup/947_sales_quote_approvals.sql
-- Migration:     947
-- Purpose:       报价系统 MVP（实施规划 W1-T2，决议 B——DealHub 三维阈值
--                + 「只审例外」）：
--                sales_quote_approvals —— 人审记录。三维全不触发 ⇒ 直发
--                不进本表（DealHub：政策内自动放行）；进本表 = 触发了
--                至少一维。trigger_reasons jsonb 记「哪三维、各自读数、
--                各自阈值」——审计可复算（突破率自校准的数据面）。
--                MVP 单级审批（super / 运营负责人）。
--
--                ★ 三维（决议 B）：毛利下限 / 折扣深度 / 单笔金额——
--                  阈值存 settings_kv（spec 登记），不落本表；本表只记
--                  触发时刻的读数与阈值快照（事后阈值改了，历史单的
--                  触发原因仍可复算）。
--
--                定位：自营内部运营表 ⇒ 不挂 tenant_isolation_* RLS
--                （风暴档 §5 预登记）。
--
-- Down: 947_sales_quote_approvals.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 942–946 形态；★ 实测（2026-11-05 本地 PG17 空库）：
--   新建空表 + 2 索引 <0.05s（无存量可扫）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.sales_quote_approvals (
    id              bigserial PRIMARY KEY,
    quote_id        bigint NOT NULL,
    version_id      bigint NOT NULL,
    -- 触发原因（可复算）：[{dimension:"margin_floor"|"discount_depth"|
    --   "order_amount", value:<读数>, threshold:<阈值>}, ...]
    trigger_reasons jsonb NOT NULL DEFAULT '[]'::jsonb,
    requested_by    text NOT NULL DEFAULT '',
    -- MVP 单级审批：approved | rejected
    decision        text NOT NULL DEFAULT 'pending',
    approver        text NOT NULL DEFAULT '',
    comment         text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    decided_at      timestamptz,
    -- 耗时秒（decided_at - created_at，落库时算好——Odoo store=True 范式）
    elapsed_seconds int,
    CONSTRAINT sales_quote_approvals_decision_check
        CHECK (decision IN ('pending', 'approved', 'rejected'))
);

CREATE INDEX IF NOT EXISTS idx_sqa_quote
    ON public.sales_quote_approvals (quote_id);
CREATE INDEX IF NOT EXISTS idx_sqa_decision
    ON public.sales_quote_approvals (decision, created_at);

COMMENT ON TABLE public.sales_quote_approvals IS
    'Human approval records for the sales MVP (947, Decision B). A row exists only when at least one of the three DealHub dimensions (margin floor / discount depth / order amount) triggered — in-policy deals auto-approve and never land here. trigger_reasons carries each dimension''s reading and threshold snapshot at trigger time so decisions stay recomputable after thresholds change.';
COMMENT ON COLUMN public.sales_quote_approvals.trigger_reasons IS
    'Array of {dimension, value, threshold} objects — DealHub three-dimension trigger evidence, recomputable (audit red line).';
COMMENT ON COLUMN public.sales_quote_approvals.elapsed_seconds IS
    'Precomputed (decided_at - created_at) seconds for the approval-SLA funnel; NULL while pending.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('947', 'sales_quote_approvals: single-level human approval records with recomputable trigger_reasons jsonb (DealHub three-dimension snapshots: margin floor / discount depth / order amount), decision, approver, elapsed seconds; internal ops table, no tenant RLS')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
