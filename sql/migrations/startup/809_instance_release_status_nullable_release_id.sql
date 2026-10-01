-- ===========================================================================
-- File:          sql/migrations/startup/809_instance_release_status_nullable_release_id.sql
-- Migration:     809
-- Database:      llm_gateway
-- Purpose:       解除 instance_release_status.release_id 的 NOT NULL，让
--                「上报的版本还没有对应 releases 行」成为可表达的状态
--
-- Status:        active
-- Idempotent:    YES (DROP NOT NULL 可重复执行)
--
-- Background:
--   Round 44 报告 §3 记录了一个「真产品 bug」：autoupdate 的
--   RecordUpdateReport 在 `SELECT id FROM releases WHERE version = $1`
--   查不到时把 release_id 兜底成 0，而 instance_release_status.release_id
--   带 `REFERENCES releases(id)` 外键、releases_id_seq 恒从 1 开始，
--   于是这条兜底**每次必撞 23503**，handler 直接回 500，上报丢失。
--
--   本轮把根因又挖深了一层。379_instance_release_status.sql 本来就是
--   为修这件事写的，它声明：
--
--     release_id BIGINT,                                  -- 可空
--     ...
--     CREATE INDEX idx_irs_release ON instance_release_status (release_id)
--       WHERE release_id IS NOT NULL;                     -- 偏索引，只有可空才需要
--
--   偏索引的存在本身就证明「可空」是原始设计意图。但它**从未生效**：
--
--     1. 376_autoupdate.sql 先建了表，release_id BIGINT NOT NULL
--        REFERENCES releases(id) ON DELETE CASCADE；
--     2. 379 的 `CREATE TABLE IF NOT EXISTS instance_release_status (...)`
--        在表已存在时是**空操作**——它只写 CREATE，不写 ALTER。哪怕 379
--        被注册并执行，也改不动一个已存在的列的 NOT NULL。
--     3. 379 本身根本没注册进 StartupFiles（注册下限是 388），是个
--        潜伏的 no-op。
--   本机 llm_gateway 实测（2026-10-01）：release_id 仍是 NOT NULL，
--   外键 instance_release_status_release_id_fkey 仍在，而 idx_irs_release
--   这个偏索引**不存在**——三处互相印证 379 的可空意图从未落地。
--
--   触发它的是一条真实业务流，不是 contrived 夹具：回滚上报的
--   ToVersion 是「回滚到的那个旧版本」，它天然可能没有对应的 releases 行。
--   TestPgxStore_RecordUpdateReport/rollback_report（to_version=v1.4.0）
--   与 TestUpgradeRetry/RetryMechanism 两个独立测试点同时命中。
--
--   本迁移只做最小且忠于原意的那一步：**去掉 NOT NULL**。
--   外键保留——外键本来就允许 NULL，保留它意味着 release_id 非空时
--   引用完整性照旧被强制，而「没有 release」用一个 NULL 表达。
--   同时补上 379 当年想建却没建成的那个偏索引。
--
-- 配套代码改动（同一批提交）：
--   autoupdate/store_pgx.go RecordUpdateReport 查不到 release 时改用
--   NULL 而不是 0。只改 schema 不改代码的话，那条兜底仍然会插 0 并
--   继续撞外键——两处必须一起改。
--
-- Down: 809_...down.sql 恢复 NOT NULL 并删除偏索引（注意：若已有
--       release_id IS NULL 的行，回滚会失败，属于预期的显式拒绝）。
-- ===========================================================================

-- 1. 解除 NOT NULL（外键保留，允许 NULL）
ALTER TABLE public.instance_release_status
  ALTER COLUMN release_id DROP NOT NULL;

-- 2. 补上 379 当年声明、却因 CREATE TABLE IF NOT EXISTS 空操作而
--    从未存在的偏索引。对可空列建部分索引是标准写法：NULL 行不参与。
CREATE INDEX IF NOT EXISTS idx_irs_release
  ON public.instance_release_status (release_id)
  WHERE release_id IS NOT NULL;
