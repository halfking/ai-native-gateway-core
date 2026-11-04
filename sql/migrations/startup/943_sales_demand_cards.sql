-- ===========================================================================
-- File:          sql/migrations/startup/943_sales_demand_cards.sql
-- Migration:     943
-- Purpose:       报价系统 MVP（实施规划 W1-T1）：
--                sales_demand_cards —— 需求池（极简六字段，风暴档 §4-2：
--                客户 / 场景 / 月用量 / 预算 / 窗口 / 状态）。
--                ★ 943 增补（实施规划 §4）：领取人 + time-actor 到期态
--                （Sharetribe：到期是 time 转换，**读取时派生**，不用
--                定时任务刷库 —— 与报价单 Expired 派生态同款决议）。
--                月用量/预算是自由形状描述（MVP 不建计量模型）⇒ text。
--                一个需求 → 多报价版本（944.demand_card_id 可空引用）。
--
--                定位：自营内部运营表 ⇒ 不挂 tenant_isolation_* RLS
--                （风暴档 §5 预登记）；角色闸在应用层（sales 本人 +
--                super 显式越权，A-11 先例）。
--
-- Down: 943_sales_demand_cards.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 942 形态；★ 实测（2026-11-05 本地 PG17 空库）：
--   新建空表 + 2 索引 <0.05s（无存量可扫）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.sales_demand_cards (
    id            bigserial PRIMARY KEY,
    customer_name text NOT NULL,
    scenario      text NOT NULL DEFAULT '',
    -- 月用量 / 预算：自由描述（"500M in + 100M out / 月"、"$2k / 月"），
    -- MVP 不建结构化计量模型（Lago graduated/volume 需独立裁决）。
    monthly_usage text NOT NULL DEFAULT '',
    budget        text NOT NULL DEFAULT '',
    -- 期望时间窗（自由文本；正式有效期语义在报价单 945.valid_until）
    window_hint   text NOT NULL DEFAULT '',
    -- open → claimed → quoted → won | lost；closed = 放弃/无效。
    -- expired **不落库**：读取时 expires_at < now() 派生（time-actor）。
    status        text NOT NULL DEFAULT 'open',
    claimed_by    text NOT NULL DEFAULT '',
    claimed_at    timestamptz,
    expires_at    timestamptz,
    lost_reason   text NOT NULL DEFAULT '',
    created_by    text NOT NULL DEFAULT '',
    notes         text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sales_demand_cards_status_check
        CHECK (status IN ('open', 'claimed', 'quoted', 'won', 'lost', 'closed'))
);

CREATE INDEX IF NOT EXISTS idx_sdc_status ON public.sales_demand_cards (status);
CREATE INDEX IF NOT EXISTS idx_sdc_customer ON public.sales_demand_cards (customer_name);

COMMENT ON TABLE public.sales_demand_cards IS
    'Demand-pool cards for the sales quotation MVP (943): six minimal fields (customer / scenario / monthly usage / budget / window / status) plus claimant and time-actor expiry. Internal ops table: no tenant RLS (pre-registered), role gate at the application layer (sales claims own cards; super_admin may act explicitly).';
COMMENT ON COLUMN public.sales_demand_cards.status IS
    'open | claimed | quoted | won | lost | closed. Expired is NOT a stored state: derived at read time as expires_at < now() (Sharetribe time-actor transition, no cron/state-flush race).';
COMMENT ON COLUMN public.sales_demand_cards.lost_reason IS
    '强制回填面（风暴档 §4-5）：won/lost 收口时必须写明丢单/成单原因。';

INSERT INTO public.schema_migrations (version, description)
VALUES ('943', 'sales_demand_cards: demand-pool six-field cards + claimant + time-actor expiry (expires_at derived at read time, not a stored state); internal ops table, no tenant RLS')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
