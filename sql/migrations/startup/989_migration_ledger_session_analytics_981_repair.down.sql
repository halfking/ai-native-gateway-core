-- ===========================================================================
-- File:          sql/migrations/startup/989_migration_ledger_session_analytics_981_repair.down.sql
-- Migration:     989 (down)
-- Purpose:       撤销 989 的记账回填（只删台账行，**不动任何对象** ——
--                981 的产物本来就在场，回填只是把「已跑过」记进账本；
--                删掉这一行等于把 980→990 的断层还回去）。
--                ★ 与 960 down 同款纪律：down 只退记账，不退对象
--                  （980/990 等被回填的迁移各自负责退自己的对象）。
--
-- ★ 回滚提示：down 之后 211 的记账链会**重新出现 '981' 的断层**
--   ⇒ 若只是为了「把 981 的记账行改回去」，更稳的处置是**手工改描述**
--   （UPDATE public.schema_migrations SET description = … WHERE version='981'）
--   而不是整体回退本迁移。
--
-- Down: 989_migration_ledger_session_analytics_981_repair.down.sql
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';

-- 撤掉本迁移自身的登记
DELETE FROM public.schema_migrations WHERE version = '989';

-- 撤掉 981 的回填行（★ 只删本迁移写进去的那一条；本迁移是唯一写它的地方）
DELETE FROM public.schema_migrations
WHERE version = '981'
  AND description LIKE '%ledger repair: 981 file lacked self-registration%';

COMMIT;
