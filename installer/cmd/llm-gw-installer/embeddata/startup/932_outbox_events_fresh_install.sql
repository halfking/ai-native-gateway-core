-- ===========================================================================
-- File:          sql/migrations/startup/932_outbox_events_fresh_install.sql
-- Migration:     932
-- Purpose:       fresh-install 链补齐 outbox_events（P0-3 live E2E 抓到的
--                真实缺口）：该表在 211 是部署后手工补建的（V357，应用侧
--                DDL），从未进入 installer 登记序 ⇒ 全新安装的库没有它，
--                outbox dispatcher 与 userscript capture 的同事务事件写入
--                全部 42P01。幂等：已有环境（211/252）no-op。
-- Down: 932_outbox_events_fresh_install.down.sql（只删本迁移登记，不删表）
-- ===========================================================================

BEGIN;

-- ★ 锁与超时守卫（2026-11-02 第二十六轮补齐；★ 本迁移在改号后**仍然没有**）：
--   ★ 缺口本体（三道门各判一次红，缺一不可）：
--     ① `TestHeavyDDLModernMigrationsDeclareMeasuredBudget`：本文件含**不带
--        CONCURRENTLY 的 CREATE INDEX** ⇒ 按门判据算全表扫描类 DDL ⇒ 必须
--        显式 SET statement_timeout ≥ 10min，并写明「实测」——没有量过的
--        预算就是拍脑袋，下一个环境会替你重新发现它。
--     ② `startup_ddl_lock_guard_gate_test`：870 之后的新迁移含强锁 DDL
--        必须自带 lock_timeout ⇒ 抢不到锁就快速失败，而不是排队堵住全库。
--     ③ `startup_setlocal_effective_gate_test`：SET LOCAL 必须落在**事务块内**
--        ⇒ 写在 BEGIN 之前只是 WARNING，守卫等于空文。
--   ★ 实测（2026-11-02，本地 PG 17.10 / docker postgres:17-alpine，真实跑脚本）：
--     全新空库跑完整脚本墙钟 0.128s（两次独立测量 0.128 / 0.202s）；最坏形态（表已存在 + 10 万行 +
--     索引缺失 ⇒ 强制重建索引）0.180s ⇒ 与 10min 预算差**三个数量级**。
--   ★ 结构理由同 912/913/928：本文件的每个 CREATE INDEX 都建在**本文件刚
--     CREATE 的空表**上（无存量可扫）⇒ 抬到 10min 也不会让它更安全，只会
--     给后人「这张表有百万级存量」的错觉。
--   ★ 锁面：CREATE TABLE / CREATE INDEX / ALTER TABLE ENABLE RLS 各拿一次锁；
--     自带 lock_timeout ⇒ 拿不到锁快速失败，不排队堵住写路径。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.outbox_events (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    schema_version INT NOT NULL,
    tenant_id TEXT NOT NULL,
    aggregate_id TEXT NOT NULL DEFAULT '',
    aggregate_version INT NOT NULL DEFAULT 1,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_outbox_events_status_retry
    ON public.outbox_events (status, next_retry_at);

INSERT INTO public.schema_migrations (version, description)
VALUES ('932', 'fresh-install gap: create outbox_events if absent (hand-applied on 211 as V357, never registered in the installer chain; P0-3 live E2E caught it)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
