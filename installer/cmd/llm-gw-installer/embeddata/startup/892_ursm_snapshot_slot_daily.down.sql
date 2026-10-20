-- 892 down: 删除 P11 的每日 slot 汇总表与它的填充函数。
--
-- ⚠ 回滚后**巡检的成本会回到「每小时扫 7 天」**（实测每次 2.9 GB / 190 s，§143.29 成本 #1）。
--
--   回滚的语义影响：
--   ① 若巡检脚本**已经**改成读 ursm_snapshot_slot_daily ⇒
--      回滚这张表会让巡检**当场查不到基线** ⇒ 所有桶判成 NO-BASELINE
--      ⇒ 巡检会「安静地不再告警」⇒ ★★ 这是比「变慢」更坏的一种失败
--      ⇒ ⇒ ★★★ **回滚顺序必须是：先把巡检脚本改回读源表，再回滚本迁移。**
--   ② 若巡检脚本**还没**改（纯回滚本迁移）⇒ 巡检不受影响，只是回到旧成本。
--
-- ★ 因此本 down 迁移**不删巡检脚本的任何东西** —— 脚本的回退是独立的部署动作。
--
-- 幂等：IF EXISTS。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '300s';

DROP FUNCTION IF EXISTS public.refresh_ursm_snapshot_slot_daily(integer);
DROP TABLE IF EXISTS public.ursm_snapshot_slot_daily;

COMMIT;
