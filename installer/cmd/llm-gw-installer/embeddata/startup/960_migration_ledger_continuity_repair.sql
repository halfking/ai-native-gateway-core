-- ===========================================================================
-- File:          sql/migrations/startup/960_migration_ledger_continuity_repair.sql
-- Migration:     960
-- Purpose:       记账连续性修复（2026-11-12 A-17 收口轮批判复审发现）：
--                950 / 951 / 957 / 958 四个迁移的文件**缺
--                `INSERT INTO public.schema_migrations` 自登记行**
--                （952–956/959 起的现行约定，这四个由部署修订序列应用、
--                文件没写 INSERT）⇒ 211/245/252 的 schema_migrations 记账链
--                出现 949→952→959 式**三处断层**。
--                后果 = 832 自愈轮同款的**记账面**版本（对象本身已在场、
--                无功能漂移）：台账/验收门按 version 连续性对账时读不到 ⇒
--                「对象在但记账缺」；文件链水位线（max(version)）掩盖之下，
--                下次网关启动会继续跳过这四个文件（LESSONS §687 同机制）。
--                修法（832 先例形态：守卫式回填）：
--                  · 每行回填带**对象在场守卫**（约束/索引/表真在场才记账）
--                    ⇒ 幂等、可重放、fail-safe；
--                  · ON CONFLICT DO UPDATE（重放安全）；
--                  · 本文件自身自登记（≥952 约定）⇒ 记账链闭合。
--                门：TestMigrationLedgerSelfRegistration（≥952 的迁移文件必须
--                    自登记；957/958 豁免 = 字节冻结 + 由本迁移修复）。
--
-- Down: 960_migration_ledger_continuity_repair.down.sql
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- 950：credential_probe_queue.source 七值闭集（对象守卫=约束定义含 credential_recovery）
INSERT INTO public.schema_migrations (version, description)
SELECT '950', 'credential_probe_queue.source CHECK closure + credential_recovery (ledger repair: file lacked self-registration)'
WHERE EXISTS (
    SELECT 1 FROM pg_constraint c
    WHERE c.conrelid = 'public.credential_probe_queue'::regclass
      AND pg_get_constraintdef(c.oid) LIKE '%credential_recovery%')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

-- 951：armor_judgments (created_at) 前导索引（对象守卫=索引在场）
INSERT INTO public.schema_migrations (version, description)
SELECT '951', 'armor_judgments (created_at) leading index (ledger repair: file lacked self-registration)'
WHERE to_regclass('public.idx_armor_judgments_created_at') IS NOT NULL
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

-- 957：self_check_runs.error_type CHECK 闭集补 rate_limit_observed
INSERT INTO public.schema_migrations (version, description)
SELECT '957', 'self_check_runs.error_type CHECK closure + rate_limit_observed (ledger repair: file lacked self-registration)'
WHERE EXISTS (
    SELECT 1 FROM pg_constraint c
    WHERE c.conrelid = 'public.self_check_runs'::regclass
      AND pg_get_constraintdef(c.oid) LIKE '%rate_limit_observed%')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

-- 958：market_platform_registry（A-17 区域/币种裁定权威 + is_ours）
INSERT INTO public.schema_migrations (version, description)
SELECT '958', 'market_platform_registry (A-17 region/currency authority + is_ours; ledger repair: file lacked self-registration)'
WHERE to_regclass('public.market_platform_registry') IS NOT NULL
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

-- 自登记（≥952 约定）
INSERT INTO public.schema_migrations (version, description)
VALUES ('960', 'migration ledger continuity repair: guarded backfill of schema_migrations rows for 950/951/957/958 (files lacked self-registration; guards = live objects)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
