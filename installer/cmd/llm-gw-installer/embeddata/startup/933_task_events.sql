-- ===========================================================================
-- File:          sql/migrations/startup/933_task_events.sql
-- Migration:     933
-- Purpose:       taskbus 的 task_events 表（P1-1 S1 后半，ADR
--                docs/adr/2026-11-02-outbox-consolidation-via-taskbus.md）。
--                目标表形态与 internal/taskbus/doc.go 的规划一致；列语义
--                复刻 MemoryStore：pending/failed 且 next_retry_at 到期可
--                claim；sent/dlq 是终态（回收窗口的基准 = completed_at）。
--
-- Down: 933_task_events.down.sql
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
--     全新空库跑完整脚本墙钟 0.126s（两次独立测量 0.126 / 0.146s）；最坏形态（表已存在 + 10 万行 +
--     索引缺失 ⇒ 强制重建索引）0.127s ⇒ 与 10min 预算差**三个数量级**。
--   ★ 结构理由同 912/913/928：本文件的每个 CREATE INDEX 都建在**本文件刚
--     CREATE 的空表**上（无存量可扫）⇒ 抬到 10min 也不会让它更安全，只会
--     给后人「这张表有百万级存量」的错觉。
--   ★ 锁面：CREATE TABLE / CREATE INDEX / ALTER TABLE ENABLE RLS 各拿一次锁；
--     自带 lock_timeout ⇒ 拿不到锁快速失败，不排队堵住写路径。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS public.task_events (
    id              BIGSERIAL PRIMARY KEY,
    task_type       TEXT NOT NULL,
    payload         BYTEA NOT NULL,
    payload_version INT NOT NULL DEFAULT 1,
    idempotency_key TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    next_retry_at   TIMESTAMPTZ,
    last_error      TEXT,
    claimed_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    CONSTRAINT uq_task_events_idem UNIQUE (task_type, idempotency_key),
    CONSTRAINT ck_task_events_status CHECK (status IN ('pending','failed','dlq','sent'))
);
CREATE INDEX IF NOT EXISTS idx_task_events_claim
    ON public.task_events (status, next_retry_at)
    WHERE status IN ('pending','failed');

INSERT INTO public.schema_migrations (version, description)
VALUES ('933', 'taskbus task_events table (P1-1 S1: idempotent enqueue via (task_type,idempotency_key) unique, claim index on pending/failed + next_retry_at, completed_at as retention base)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
