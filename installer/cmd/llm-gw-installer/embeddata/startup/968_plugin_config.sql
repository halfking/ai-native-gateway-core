-- ===========================================================================
-- File:          sql/migrations/startup/968_plugin_config.sql
-- Migration:     968
-- Purpose:       plugin_config 表——插件 provider 选择（Native / Casdoor）的持久化。
--
-- ── 背景（★ 「DDL 落在死通道」＝表在活库中根本不存在）──────────────────────
--
-- plugin-architecture落地线（3f7f0dd5c → 240026a95）新增了
-- cmd/gateway/plugin_init.go 的 loadConfigFromDatabase()，它读
-- `plugin_config` 表；而该表的 DDL 被写在了**顶层 migrations/** 目录下
-- （migrations/add_plugin_config.sql）——
--   ★ 顶层 migrations/ 是**死文件区**（历史 035 的教训：撞号异文件、
--     无执行器接线、手工执行会与正典通道的同号变更冲突）；
--   ★ db/migrations/ 的 README 更是明令「禁止新增与手工执行」；
--   ★ 两份 DDL 都**不在** startup 正典通道 ⇒ 全新安装 / 211 升级序列
--     **永远不会建这两张表**。
--
-- ★ 后果分两级：
--   ① plugin_config：loadConfigFromDatabase 查询报42P01 ⇒ 代码走
--      `slog.Debug("no plugin config in database, using defaults")`
--      ⇒ 静默回落 default（全部 native）⇒ **恰好是 fail-safe**
--      （数据库不可用时代理选择器读不到配置，回落原生实现）；
--      但运维在 admin 面上改了 provider 选择，重启后**丢失**
--      （PUT写库失败会回 500，但「写成功但表不存在」的路径不存在——
--      实际是 PUT 直接500，管理面不可用）。
--   ② approval_casdoor_mapping：internal/approval.PgxApprovalStore 的
--      **写面**（Save/Update 都 INSERT/UPDATE 这张表）⇒ Casdoor 审批
--      真正启用时，第一个 Save 就报 42P01（relation does not exist）
--      ⇒ **审批链路直接不可用**（P1）。
--
-- ── 修法 ────────────────────────────────────────────────────────────────
--
--   把两份 DDL 收编进 startup 正典通道（968 = plugin_config，
--   969 = approval_casdoor_mapping），并按五点同步纪律登记：
--     ① sql/migrations/startup/968_*.sql（本文件）
--     ② sql/migrations/startup/968_*.down.sql
--     ③ sql/schema/installed_startup_migrations.tsv
--     ④ installer/internal/dbinit/runner.go 的 StartupFiles
--     ⑤ installer/cmd/llm-gw-installer/main.go 的 //go:embed +
--        embeddedSQLFiles + embeddata/startup/ 双副本
--     ⑥ docs/db-changelog.md 台账行
--     ⑦ scripts/apply-db-revision-sequence.sh 的 211 通道 files 数组
--        + ledger_artifact_probe 产物断言
--
-- ── 幂等性与回放 ────────────────────────────────────────────────────────
--
--   · CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS ⇒ 重放无副作用。
--   · 本文件不含不可事务化语句（CREATE INDEX CONCURRENTLY 等）⇒ 可单事务
--     执行，无需逐文件事务豁免（标记语义见 installer/internal/dbinit）。
--   · ★ RLS 策略与触发器用 DO $$ 包裹 + IF NOT EXISTS 判据（PG 的
--     CREATE POLICY / CREATE TRIGGER 没有 IF NOT EXISTS 语法）⇒ 幂等。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- ── 表（幂等）────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.plugin_config (
    id                INTEGER PRIMARY KEY DEFAULT 1,
    auth_provider     VARCHAR(50) NOT NULL DEFAULT 'native',
    user_manager      VARCHAR(50) NOT NULL DEFAULT 'native',
    tenant_manager    VARCHAR(50) NOT NULL DEFAULT 'native',
    approval_provider VARCHAR(50) NOT NULL DEFAULT 'native',
    created_at        TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMP NOT NULL DEFAULT NOW(),

    -- 单例行：只允许 id=1
    CONSTRAINT single_config_row CHECK (id = 1),
    -- provider 取值域（与 admin/plugin_config_handler.go 的
    -- validateProviderSelection 白名单一致：native / casdoor）
    CONSTRAINT valid_auth_provider      CHECK (auth_provider IN ('native', 'casdoor')),
    CONSTRAINT valid_user_manager       CHECK (user_manager IN ('native', 'casdoor')),
    CONSTRAINT valid_tenant_manager     CHECK (tenant_manager IN ('native', 'casdoor')),
    CONSTRAINT valid_approval_provider  CHECK (approval_provider IN ('native', 'casdoor'))
);

CREATE INDEX IF NOT EXISTS idx_plugin_config_updated ON public.plugin_config(updated_at);

-- 默认配置（全部 native）——ON CONFLICT 幂等
INSERT INTO public.plugin_config (id, auth_provider, user_manager, tenant_manager, approval_provider)
VALUES (1, 'native', 'native', 'native', 'native')
ON CONFLICT (id) DO NOTHING;

COMMENT ON TABLE public.plugin_config IS
 'Plugin provider configuration - stores selection between Native (PostgreSQL) and Casdoor providers';
COMMENT ON COLUMN public.plugin_config.id IS 'Singleton row id (always 1)';
COMMENT ON COLUMN public.plugin_config.auth_provider IS 'Authentication provider: native or casdoor';
COMMENT ON COLUMN public.plugin_config.user_manager IS 'User management provider: native or casdoor';
COMMENT ON COLUMN public.plugin_config.tenant_manager IS 'Tenant manager provider: native or casdoor';
COMMENT ON COLUMN public.plugin_config.approval_provider IS 'Approval workflow provider: native or casdoor';

COMMIT;
