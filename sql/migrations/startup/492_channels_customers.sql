-- W4.3: 渠道/客户视角 —— 渠道商（channel）→ 客户（customer）→ 租户/应用的归属层级。
--
-- 事实源对齐：
--   * applications.customer_id（迁移 407）是「应用→客户」的既有锚点，
--     本迁移把 customer 落成实表并反向引用它，不另开新口径。
--   * 渠道商门户的权限核心是 OwnsCustomer(channel, customer)：渠道商只能看
--     自己名下客户的会话/成本数据（跨客户访问 403）。
--
-- 设计边界（诚实登记）：
--   * customer_id 是 BIGINT 且现状无外键（applications.customer_id 无 REFERENCES），
--     本表同样不强加 FK —— 历史数据里 customer_id 可能指向不存在的行，
--     加 FK 会让存量环境启动迁移直接失败。对账用
--     channels_customers 对 customers 的 FK（新表新约束）兜住「渠道挂载」面。
--   * channel 不引入新身份系统：渠道商登录复用 users 表（role='channel'），
--     users.username 即渠道商标识（W4.3 v1 不做渠道商自助注册）。
--
-- ★ 2026-11-26 审计轮代上游收口（★ 零已应用字节变更：★ 本迁移在本轮之前【从未被任何通道应用过】
--   —— 154 生产库 public.schema_migrations 无 '492' 行、public.channels 与 public.channel_users
--   均不存在（2026-11-26 只读实测）★ ⇒ ★ 改它不触及任何环境的已应用字节 ★★）：
--   ① 加事务包裹（★ BEGIN; … COMMIT; ★）——★ 仓内增量口径要求「基线之后新增的启动迁移必须事务包裹」，
--      判据见 scripts/audit/asset/multi_statement_migration_baseline.ref 的文件头 ★★；
--   ② 加台账自登记（★ INSERT INTO public.schema_migrations … ★）——★ 仓内「≥952 双账本纪律」：
--      迁移必须自己把行写进 public.schema_migrations，★ 否则任何通道应用后运行时台账无行 ⇒
--      连续性门／漂移报告假红 ★★。
--   ★ 未动 DDL 本体（★ 表／索引／注释逐字保留 ★）。

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.channels (
    id           BIGSERIAL PRIMARY KEY,
    name         VARCHAR(128) NOT NULL,
    code         VARCHAR(64)  NOT NULL UNIQUE,       -- 渠道商外部标识（门户 URL / 日志检索用）
    contact      VARCHAR(256) NOT NULL DEFAULT '',
    enabled      BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS public.customers (
    id           BIGSERIAL PRIMARY KEY,
    name         VARCHAR(128) NOT NULL,
    code         VARCHAR(64)  NOT NULL UNIQUE,       -- 客户外部标识
    channel_id   BIGINT NOT NULL REFERENCES public.channels(id) ON DELETE RESTRICT,
    enabled      BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customers_channel ON public.customers (channel_id);

-- 渠道商用户 → 渠道归属（users 表复用，role='channel'）。
-- 一人一渠道（v1）；跨渠道代理留到有真实需求再演进。
CREATE TABLE IF NOT EXISTS public.channel_users (
    user_id     INTEGER NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    channel_id  BIGINT  NOT NULL REFERENCES public.channels(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, channel_id)
);

COMMENT ON TABLE public.channels IS 'W4.3 渠道商（S2 门户）：销售代理层级，拥有若干客户';
COMMENT ON TABLE public.customers IS 'W4.3 客户：渠道商名下的最终客户；applications.customer_id 指向本表';
COMMENT ON TABLE public.channel_users IS 'W4.3 渠道商登录用户 → 渠道归属（users.role=channel）';

-- 台账自登记（★ ≥952 双账本纪律：★ 迁移必须自己把行写进 public.schema_migrations ★★）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('492', 'W4.3 channels/customers/channel_users: 渠道商 → 客户 → 租户/应用归属层级')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
