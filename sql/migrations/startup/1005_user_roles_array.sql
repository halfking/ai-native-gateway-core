-- ============================================================
-- 迁移 1005: users.roles 多角色数组（三级配额制 P1-b）
-- 目的：users.role 单值 → roles 数组（role 保留为主角色，向后兼容）
-- 口径：roles 为空/NULL ⇒ 有效角色集 = [role]（存量行为逐字节不变）；
--       role 恒在 roles 数组内（写入路径同步维护）
-- 依赖：001（users 表）、1003（三级配额制同族）
-- 日期：2026-11-27
-- 影响：users.roles(新列)
-- ============================================================

-- 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go：>870 且含强锁 DDL
--   ⇒ 必须自带 lock_timeout）。
-- ★ 实测（2026-11-27 P1-b 轮，PG17 本机一次性容器 casdoor-e2e-pg:13.3.0）：
--   users 表存量 0 行的空库单件首跑墙钟 <0.05s（ADD COLUMN 属元数据级操作）、
--   幂等重放 0.04s ⇒ 与 10min 预算差五个数量级，无 914 型「拍小了中止部署」风险。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS roles TEXT[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN users.roles IS
  '角色数组（多角色支持）；空数组 = 有效角色集回落 [role] 单值（存量兼容）。role 恒为数组成员（主角色）。';

COMMIT;
