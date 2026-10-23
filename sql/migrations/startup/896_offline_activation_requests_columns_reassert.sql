-- ===========================================================================
-- File:          sql/migrations/startup/896_offline_activation_requests_columns_reassert.sql
-- Migration:     896
-- Database:      llm_gateway
-- Purpose:       把 375 / 376 给 offline_activation_requests 补的三列
--                （status / reject_reason / activation_code）与两个索引
--                **重新交付到只靠升级通道维护的存量库**（生产 252 实测缺列）。
--
-- 缺陷机制（2026-10-21 252 PG 日志审计轮实测，★ 一句话版）：
--   **生产升级通道有一道 412 的地板，375/376 在地板下面。**
--
--   · 生产 154/245 的部署走 scripts/deploy-lib/db-changelog.sh，它只把
--     version >= DB_LEDGER_RECONCILE_FROM（默认 412）且不在 schema_migrations
--     的 startup 文件当 pending ⇒ 375 / 376（< 412）对该通道**永远不可见**。
--   · 2026-10-12 把它们补登进 scripts/apply-db-revision-sequence.sh 的 files=()
--     只修了 docker 路径（deploy-seamless-docker.sh）与新装；252 上该通道的账本
--     gateway_db_revision_sequences 停在 2026-09-12 ⇒ 生产从未收到。
--   · 结果（252 只读实测）：public.offline_activation_requests 只有 374 的 9 列
--     （id/license_key/hardware_hash/instance_id/device_name/request_id/
--      created_at/approved_at/signed_license），licensing/store_pgx.go 的离线激活
--     管理 SQL（列出 / 审批 / 驳回）在生产恒 42703
--     `column "activation_code" does not exist`（PG 日志 ×8，10-20 13:02 页面全扫触发）。
--   · 新装库由 installer 链应用 375/376 ⇒ 三列齐备 ⇒ 本迁移在新装库是 **no-op**
--     （一次性库 itgate_r145_shape 实测：fresh-has / prod-lacks 的列差集**恰好只有这三列**）。
--
-- 为什么不是下调 DB_LEDGER_RECONCILE_FROM：
--   412 以下有 100+ 个被 baseline 吸收、从未在 schema_migrations 记账的文件，
--   整体放开会让通道在生产重放上百个历史文件（含非幂等者）⇒ 爆炸半径不可控。
--   「新号重申旧内容」是本仓既有定式（683b / 803-806 收编迁移）。
--
-- 幂等：ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS；表不存在时 NOTICE 并跳过。
-- 锁：ADD COLUMN 取 ACCESS EXCLUSIVE（生产该表 0 行，毫秒级）；带 lock_timeout
--     防部署期挂在锁队列里放大为热路径停顿（878/882 款）。
-- 内容与 375:7-13 / 376:4-8 **逐字等价**（列类型、默认值、索引名与谓词均不变）。
-- 只碰 public schema：252 上另有 maintain.offline_activation_requests（42 张 maintain
-- 副本之一，形态完整），不是网关读的那张，本迁移不触碰。
-- ===========================================================================

BEGIN;
SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

DO $$
DECLARE
    present int;
BEGIN
    IF to_regclass('public.offline_activation_requests') IS NULL THEN
        RAISE NOTICE '896: public.offline_activation_requests absent (licensing schema not initialised here) - skipping';
        RETURN;
    END IF;

    -- 375_offline_requests_status.sql
    ALTER TABLE public.offline_activation_requests
        ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
    ALTER TABLE public.offline_activation_requests
        ADD COLUMN IF NOT EXISTS reject_reason TEXT;
    CREATE INDEX IF NOT EXISTS idx_oar_status ON public.offline_activation_requests (status);

    -- 376_offline_activation_code.sql
    ALTER TABLE public.offline_activation_requests
        ADD COLUMN IF NOT EXISTS activation_code TEXT;
    CREATE INDEX IF NOT EXISTS idx_oar_activation_code ON public.offline_activation_requests (activation_code)
        WHERE activation_code IS NOT NULL;

    -- 自证：三列必须在位（IF NOT EXISTS 不会报错，所以要自己查）
    SELECT count(*) INTO present
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'offline_activation_requests'
      AND column_name IN ('status', 'reject_reason', 'activation_code');
    IF present <> 3 THEN
        RAISE EXCEPTION '896 failed: offline_activation_requests has %/3 of status/reject_reason/activation_code', present;
    END IF;

    RAISE NOTICE '896: offline_activation_requests status/reject_reason/activation_code + idx_oar_status/idx_oar_activation_code asserted';
END $$;

-- Ledger self-registration（710 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('896', 're-deliver 375/376 to upgrade-channel databases: offline_activation_requests.status/reject_reason/activation_code + idx_oar_status/idx_oar_activation_code; deploy-lib/db-changelog.sh only treats version >= 412 as pending so 375/376 never reached production 252 (licensing offline-activation admin SQL 42703 column activation_code does not exist); byte-equivalent DDL, IF NOT EXISTS, no-op on fresh installs')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
