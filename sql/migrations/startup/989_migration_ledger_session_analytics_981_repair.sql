-- ===========================================================================
-- File:          sql/migrations/startup/989_migration_ledger_session_analytics_981_repair.sql
-- Migration:     989
-- Purpose:       记账连续性修复（会话分析族）：迁移 981 的文件**缺
--                `INSERT INTO public.schema_migrations` 自登记行**
--                （≥952 的现行约定）⇒ **211 的记账链在 980 与 990 之间断一格**。
--
-- ★ 实测事实（2026-11-23，211 / acc-pg17，只读）：
--   · public.schema_migrations 的版本号序列是 … 979, 980, **【无 981】**, 990, 991 …
--     ⇒ 独缺 '981'；
--   · 而 981 的**产物全部在场**：4 个物化视图（user_session_aggregates /
--     project_task_aggregates / agent_usage_aggregates / model_usage_aggregates，
--     relkind='m'）+ session_dim / session_owners 两张表 + 3 个函数
--     ⇒ ⇒ **它确实跑过，只是没记账**（与 950/951/957/958 同款断层）。
--
-- ★ 为什么是【新迁移】而不是给 981 补一句自登记：
--   981 在 211 与 252 **都已 applied**，且 252 台账
--   llm_gateway_migration_checksums 记 981 = 4b6c5fc3ef284913…，
--   **与磁盘 sha256 逐字一致** ⇒ 改 981 的字节就是 L23 同款分叉
--   （已 applied 的迁移永不重跑 ⇒ 库里旧、仓内新 ⇒ 部署在迁移校验段
--   被内容指纹 fail-closed 拦下）⇒ ⇒★ **只能另立守卫式回填**（960 先例形态）。
--
-- ★ 修法（960 同款）：
--   · 回填行带**对象在场守卫**（981 的头产物真在场才记账）⇒ 幂等、可重放、
--     fail-safe（对象不在 ⇒ 不记账 ⇒ 不会写下假陈述）；
--   · ON CONFLICT (version) DO UPDATE ⇒ 重放安全；
--   · 本文件自身自登记（≥952 约定）⇒ 记账链闭合。
--
-- 门：TestMigrationLedgerSelfRegistration（≥952 的迁移文件必须自登记；
--     981 走豁免表 = 字节冻结 + 由本迁移修复，同 957/958 先例）。
--
-- Down: 989_migration_ledger_session_analytics_981_repair.down.sql
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- 981：会话分析基础设施（4 个物化视图 + 刷新/探测函数 + sessions 四列）
-- 守卫 = 981 的头产物【全部在场】（★ 与 211 部署通道的 981 产物探针同款口径：
--   MV 用 to_regclass 判在场 —— information_schema 只收 relkind ∈ {r,v}，
--   物化视图（relkind='m'）不在里面 ⇒ 用 information_schema 判 MV 恒假，
--   见 994 探针的 2026-11-20 修正）。
INSERT INTO public.schema_migrations (version, description)
SELECT '981',
       'session_analytics_foundation (ledger repair: 981 file lacked self-registration; guard = 981 artifacts in place)'
WHERE to_regclass('public.user_session_aggregates') IS NOT NULL
  AND to_regclass('public.project_task_aggregates') IS NOT NULL
  AND to_regclass('public.agent_usage_aggregates') IS NOT NULL
  AND to_regclass('public.model_usage_aggregates') IS NOT NULL
  AND to_regclass('public.session_dim') IS NOT NULL
  AND to_regprocedure('public.refresh_session_analytics_view(text)') IS NOT NULL
  AND to_regprocedure('public.refresh_all_session_analytics_views()') IS NOT NULL
  AND to_regprocedure('public.session_analytics_is_probe(text,text,text)') IS NOT NULL
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

-- 自登记（≥952 约定）
INSERT INTO public.schema_migrations (version, description)
VALUES ('989', 'migration ledger continuity repair (session-analytics): guarded backfill of schema_migrations row for 981 (file lacked self-registration; guard = 981 artifacts in place)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
