-- 891 down: 删除两条 `WHERE cost_usd < 0` 的部分索引。
--
--   DROP INDEX IF EXISTS public.idx_session_turns_cost_negative;
--   DROP INDEX IF EXISTS public.idx_session_turns_hot_cost_negative;
--
-- 父表上的那条会**级联**删掉全部分区侧子索引（877 台账口径）。
--
-- ⚠⚠ 回滚后**负成本查询没有任何加速手段** —— 这不是「少了个可选索引」：
--
--   ① 882 给 v1 `_request_logs` 建的那条同款索引，已随 **890 的 DROP TABLE**
--      一起消失（索引活不过它的表）；
--   ② 本迁移建的两条是**会话族上仅有的** cost_usd 索引。回滚它们之后，
--      bg/routing_health_checks.go 的 `recorded_cost_is_negative` 检查
--      （每 ~8-9 分钟跑一次）退回**全表扫描**：hot 表一次 Seq Scan，
--      父表每个分区一次 Seq Scan（本地真库实测计划，2026-10-20）。
--
--   ⇒ 若因故必须回滚，请同时接受「该检查重新变成表扫描」这一后果，
--     并优先安排重跑 891（up 是幂等的，IF NOT EXISTS）。
--
-- 幂等：IF EXISTS。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '300s';

DROP INDEX IF EXISTS public.idx_session_turns_cost_negative;
DROP INDEX IF EXISTS public.idx_session_turns_hot_cost_negative;

COMMIT;
