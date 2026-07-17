-- =============================================================================
-- Migration 423: approval_routing_rules 补 135 schema 列 + 建 credential_probe_model_log
-- Created: 2026-07-17
-- Author:  gateway maintainers (既有 ERROR 根因排查)
--
-- 背景 (2026-07-17 生产 ERROR 排查):
--   两个跨多次部署的 ERROR 根因已定位，本迁移一并修复：
--
--   (A) init approval notifier failed: column "risk_level" does not exist
--       (SQLSTATE 42703)
--     迁移 135_approval_routing.sql 与 391_route_incidents_audit_safety.sql
--     都用 CREATE TABLE IF NOT EXISTS approval_routing_rules，但列定义不同。
--     生产库先跑了 391（rule_name/rule_type/conditions/approvers），135 定义的
--     risk_level/channel_type/approver_ids/priority 缺失。而代码
--     domains/notification/routing_pgx.go:LoadRoutingRules 查询这 4 列 → 42703
--     → 审批通知功能（飞书/钉钉/企微卡片下发）在 init 时降级失效。
--     修复策略（用户批准：补列适配代码）：补 ADD COLUMN 把 135 的列加到现有表，
--     不改 Go 代码。补的列 NULL-able / 带默认值，不破坏 391 已有行；老行
--     risk_level 为 NULL → 代码 scan 出空串 → Route() 相等匹配不命中 →
--     该规则在通知路由里不生效（安全降级，由运维后续按需补录）。
--
--   (B) partition_manager: credential_probe_model_log cleanup failed:
--       relation "credential_probe_model_log" does not exist (SQLSTATE 42P01)
--     该表只存在于 installer 全量 baseline 快照（sql/schema/01-schema.sql），
--     老库（252 等既有库）从未跑过 installer，没有增量迁移建表。但迁移 391
--     注册了 cleanup_old_credential_probe_model_log() 函数（DELETE FROM 该表），
--     bg/partition_manager.go:cleanupOldCredentialProbeModelLog 每 interval 调用
--     一次就报一次。纯日志噪声，不影响路由/探测。本迁移补 CREATE TABLE。
--
-- 幂等性：全部 ADD COLUMN IF NOT EXISTS / DROP CONSTRAINT IF EXISTS /
--         CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS，可安全重跑。
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== 423 (A): approval_routing_rules 补 135 schema 列 ==='

-- risk_level / channel_type 允许 NULL（391 已有行无此值；NULL 在代码 Route()
-- 相等匹配里不命中任何 low/medium/high/critical 请求，等价于规则不生效）。
ALTER TABLE public.approval_routing_rules
    ADD COLUMN IF NOT EXISTS risk_level VARCHAR(16);

ALTER TABLE public.approval_routing_rules
    ADD COLUMN IF NOT EXISTS channel_type VARCHAR(16);

-- approver_ids 默认空数组；priority 与 135 一致 NOT NULL DEFAULT 0（老行回填 0）。
ALTER TABLE public.approval_routing_rules
    ADD COLUMN IF NOT EXISTS approver_ids JSONB DEFAULT '[]'::jsonb;

ALTER TABLE public.approval_routing_rules
    ADD COLUMN IF NOT EXISTS priority INT NOT NULL DEFAULT 0;

-- CHECK 约束与 135 对齐，但容忍 NULL（IS NULL OR ...）以便 391 老行通过。
-- 只加 risk_level 的约束（代码侧 RiskLevel 是通知路由的核心枚举键）。
ALTER TABLE public.approval_routing_rules
    DROP CONSTRAINT IF EXISTS chk_routing_risk_level;
ALTER TABLE public.approval_routing_rules
    ADD CONSTRAINT chk_routing_risk_level
    CHECK (risk_level IS NULL OR risk_level IN ('low','medium','high','critical'));

-- 辅助索引（与 135 对齐；对按 risk_level 路由的通知查询有用）。
CREATE INDEX IF NOT EXISTS idx_approval_routing_risk
    ON public.approval_routing_rules (tenant_id, risk_level) WHERE enabled = true;

\echo '--- approval_routing_rules 列已补齐 ---'

\echo '=== 423 (B): 建 credential_probe_model_log (消除周期性 42P01 噪声) ==='

-- DDL 镜像 sql/objects/tables/credential_probe_model_log.sql。
CREATE TABLE IF NOT EXISTS public.credential_probe_model_log (
    id bigint NOT NULL,
    tenant_id text NOT NULL DEFAULT 'default'::text,
    credential_id bigint NOT NULL,
    source text NOT NULL,
    old_model text,
    new_model text,
    actor text,
    reason text,
    created_at timestamp with time zone NOT NULL DEFAULT now()
);

-- 主键 + 序列 + 默认值。
-- 幂等性说明：245 与 154 共享同一个 252 PG，第一个目标部署时建表+建主键，
-- 第二个目标重跑时表与主键都已存在。PG 不支持 ADD CONSTRAINT IF NOT EXISTS，
-- 所以用 DO 块检查 pg_constraint 是否已有同名 pkey，没有才加。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.credential_probe_model_log'::regclass
          AND conname = 'credential_probe_model_log_pkey'
    ) THEN
        ALTER TABLE public.credential_probe_model_log
            ADD CONSTRAINT credential_probe_model_log_pkey PRIMARY KEY (id);
    END IF;
END $$;

-- 序列对齐（baseline 用独立 seq；IF NOT EXISTS 幂等）。
CREATE SEQUENCE IF NOT EXISTS public.credential_probe_model_log_id_seq;
ALTER SEQUENCE public.credential_probe_model_log_id_seq
    OWNED BY public.credential_probe_model_log.id;
-- 仅在 id 列还没有默认值时设置，避免覆盖运维侧已设的默认值。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_attrdef
        WHERE adrelid = 'public.credential_probe_model_log'::regclass
          AND adnum = (SELECT attnum FROM pg_attribute
                       WHERE attrelid = 'public.credential_probe_model_log'::regclass
                         AND attname = 'id')
    ) THEN
        ALTER TABLE public.credential_probe_model_log
            ALTER COLUMN id SET DEFAULT nextval('public.credential_probe_model_log_id_seq');
    END IF;
END $$;

-- partition_manager cleanup 走 created_at 过滤，建时间索引。
CREATE INDEX IF NOT EXISTS idx_credential_probe_model_log_created
    ON public.credential_probe_model_log (created_at);

\echo '--- credential_probe_model_log 已建 ---'

\echo '=== 423 完成 ==='
