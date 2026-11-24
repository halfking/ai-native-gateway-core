-- ===========================================================================
-- File:          sql/migrations/startup/056_user_teams.down.sql
-- Migration:     056 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚 user_teams（团队功能底座四件套之一）
--
-- ★ 2026-11-24 合并轮二十·teams 族归位（★ 原 055-058 ⇒ 曾用 982-985 ⇒ 918/925/973/986
--   ⇒ 1000-1003 ⇒ **归位回上游的 055/056/057/058**）：
--   ★ 上游已把本族**完整登记进三条通道**（deploy files=() / installer embeddata＋
--     StartupFiles / installed_startup_migrations.tsv）⇒ ★★ 本族**沿用上游编号**
--     ★★（★ ★ 三条硬证据：★ ① 四位数对 `^[0-9]3_` 守卫隐形 ⇒ 1000-1003
--     ★★ **永不被投递**；★ ② 上游版本**生产 211 已 applied** ⇒ 改字节＝L23 分叉；
--     ★ ③ 两套对象集合**逐件完全一致**）★★
--   ★ ★ **本 down 脚本是【新增文件】、★ 不是对已 applied 的 canonical 改字节**
--     ★ ⇒ ★ **不构成 L23 分叉** ★★ ⇒ ★ 上游那轮**缺这一面**、★ 本轮补齐。
--   ★ 只退**本迁移自己引入**的部分；★ 台账回退与 up 的自登记对偶。
--   ★ 幂等：IF EXISTS ＋ DELETE ... WHERE version = '056' ⇒ 重放安全。
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- 删除表（★ CASCADE 一并带走本表上的策略）
DROP TABLE IF EXISTS public.user_teams CASCADE;

-- 台账回退（与 up 的自登记对偶 —— down 只退自己引入的行）
DELETE FROM public.schema_migrations WHERE version = '056';

COMMIT;
