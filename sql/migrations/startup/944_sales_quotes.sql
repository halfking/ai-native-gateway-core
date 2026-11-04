-- ===========================================================================
-- File:          sql/migrations/startup/944_sales_quotes.sql
-- Migration:     944
-- Purpose:       报价系统 MVP（实施规划 W1-T1，决议 A——ERPNext/Invoice
--                Ninja 双源形态）：
--                ① sales_quotes —— 报价单头：客户、状态、当前版本指针
--                  + locked 正交布尔（Odoo：锁定不是状态，审批中
--                  locked=true，审批通过解锁进 sent）+ created_by。
--                ② sales_quote_status_log —— 全部状态迁移落审计
--                  （actor/from/to/at/reason），**状态机由这张转换表
--                  隐式定义**（Sharetribe 转换表范式）。
--
--                ★ 状态机（风暴档 §5）：
--                  draft → pending_approval → approved → sent
--                        → won | lost；superseded（版本被新版本替代时
--                  头回到旧语义由 current_version_id 指针表达）。
--                ★ **expired 不落库**（Invoice Ninja）：读取时按
--                  current 版本 valid_until < now() 现算（派生态，
--                  消灭定时刷状态与刷写竞态）——CHECK 闭集故意不含
--                  'expired'，误写将被 23514 拒绝。
--
--                current_version_id 的 FK 在 945 里回补（版本表后建，
--                避免本迁移前向引用不存在的表）。
--
--                定位：自营内部运营表 ⇒ 不挂 tenant_isolation_* RLS
--                （风暴档 §5 预登记）；角色闸在应用层。
--
-- Down: 944_sales_quotes.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 942/943 形态；★ 实测（2026-11-05 本地 PG17 空库）：
--   两张空表 + 1 索引 <0.05s（无存量可扫）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.sales_quotes (
    id                bigserial PRIMARY KEY,
    demand_card_id    bigint,
    customer_name     text NOT NULL,
    title             text NOT NULL DEFAULT '',
    -- 头状态；expired 是派生态**故意不在闭集里**（决议 A）。
    status            text NOT NULL DEFAULT 'draft',
    current_version_id bigint,
    -- Odoo locked 正交布尔：审批中 true（draft 内容可反复改；locked=true
    -- 起禁止改内容）；审批通过解锁进 sent。sent 后任何改动 = 新版本行。
    locked            boolean NOT NULL DEFAULT false,
    created_by        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sales_quotes_status_check
        CHECK (status IN ('draft', 'pending_approval', 'approved',
                          'sent', 'won', 'lost', 'superseded'))
);

CREATE INDEX IF NOT EXISTS idx_sales_quotes_status
    ON public.sales_quotes (status);
CREATE INDEX IF NOT EXISTS idx_sales_quotes_demand
    ON public.sales_quotes (demand_card_id);

COMMENT ON TABLE public.sales_quotes IS
    'Quotation header for the sales MVP (944). Versioning per Decision A: draft is freely editable; from pending_approval on content is frozen (locked=true, Odoo orthogonal boolean); after sent any change means a NEW immutable version row (945) + the old one superseded — in-place UPDATE of a sent version is forbidden. current_version_id points at the live version (FK added in 945).';
COMMENT ON COLUMN public.sales_quotes.status IS
    'draft | pending_approval | approved | sent | won | lost | superseded. EXPIRED IS DELIBERATELY ABSENT (Invoice Ninja derived state): computed at read time from the current version''s valid_until, never stored — no cron flush, no flush race; writing ''expired'' is rejected by this CHECK.';
COMMENT ON COLUMN public.sales_quotes.locked IS
    'Odoo-style orthogonal boolean, NOT a status: true while under approval (content frozen), false after unlock. Independent of status transitions.';

-- ── 状态迁移审计（状态机由这张转换表隐式定义，Sharetribe） ────────────────
CREATE TABLE IF NOT EXISTS public.sales_quote_status_log (
    id         bigserial PRIMARY KEY,
    quote_id   bigint NOT NULL,
    actor      text NOT NULL DEFAULT '',
    from_status text NOT NULL DEFAULT '',
    to_status  text NOT NULL,
    reason     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sqsl_quote
    ON public.sales_quote_status_log (quote_id, created_at);

COMMENT ON TABLE public.sales_quote_status_log IS
    'Append-only status-transition audit for sales_quotes (944, folded into the header migration per the 2026-11-04 plan §4 — no separate 949). The state machine is implicitly defined by this transition table (Sharetribe): every transition writes (actor, from, to, reason); reads that need the machine re-derive it from here.';

-- 头删除时审计行随 Cascade 清理（快照历史由版本表自行承载）。
COMMENT ON CONSTRAINT sales_quotes_status_check ON public.sales_quotes IS
    'Head state closure. expired deliberately excluded (derived state).';

INSERT INTO public.schema_migrations (version, description)
VALUES ('944', 'sales_quotes header (status CHECK without expired — derived state; locked orthogonal boolean; current_version_id pointer, FK added in 945) + sales_quote_status_log transition audit table')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
