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

-- ★★★ 2026-10-22（R75 轮）修正一处【真缺陷】：本 down 原来写的是
--   DROP FUNCTION IF EXISTS public.refresh_ursm_snapshot_slot_daily(integer);
--   ★★ 但 892 明确【只建一个重载】(integer, text) —— 文件头自己写明了
--   「不要再为单参数建一个兼容重载」（两个都带默认参数时 f(1) 报 not unique，
--   本地 PG 17.10 实测）⇒ **单参数版本根本不存在**。
--   ⇒ ⇒ 原 down 的那条 DROP 是**静默空操作** ⇒ 回滚后**函数残留在库里**，
--     而表已被删 ⇒ 巡检脚本的探测会看到「表不在」而回退源表路（安全），
--     但库里留着一个**指向已删表的函数** ⇒ 后续任何 `SELECT refresh_...(1)`
--     都会报「relation does not exist」⇒ 每日刷新作业会**每次失败**。
--   ⇒ 修正为**实际存在的签名** (integer, text)。
DROP FUNCTION IF EXISTS public.refresh_ursm_snapshot_slot_daily(integer, text);
DROP TABLE IF EXISTS public.ursm_snapshot_slot_daily;

COMMIT;
