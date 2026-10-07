-- 840 down: 移除 analyze 节流槽（表与两个函数）
--
-- ⚠ 只删本迁移自建的对象，不触碰 §10.53 那把 advisory 锁
--   （它在 Go 代码里，不在本迁移的范围内）。
--
-- 删除后 Go 侧对 undefined_table(42P01) 做降级 ⇒ 行为退化为「两台各跑一遍」，
-- 即回到 2026-10-07 上线 §10.53 之后的状态（而 §10.53 之前是无锁，
-- 但 analyze 本来就只在 promote tick 末尾被调一次，实际行为差异很小）。

BEGIN;

DROP FUNCTION IF EXISTS public.complete_llm_gateway_task_slot(text);
DROP FUNCTION IF EXISTS public.claim_llm_gateway_task_slot(text, interval);
DROP TABLE IF EXISTS public.llm_gateway_task_state;

COMMIT;