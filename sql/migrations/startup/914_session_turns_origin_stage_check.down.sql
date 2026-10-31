-- ===========================================================================
-- Down 914: drop the origin_stage CHECK constraints restored by 914.
--
-- ★ 只删约束，**不动列、不动行**：
--   会话族的 origin_stage 列是 707 建的，写方（domains/session/v2/turn_writer.go）
--   一直在用它，消费方（admin/routing.go、admin/analytics.go、bg/mv_consistency.go、
--   bg/probe_policy.go）也一直在读它 ⇒ 回滚**不应把列带走**。
--
-- ★ 在**父表**上删 ⇒ PG 连带把各分区的继承副本一起删掉
--   （单独删分区上的继承约束会报 "cannot drop inherited constraint"）。
--
-- ★ 语义说明：本 down 会让 origin_stage 回到「DB 完全不拒」的状态，
--   也就是 890 退役 v1 族之后、会话族重建 CHECK 之前的那个状态。
--   它是**恢复旧行为**，不是「再退役一次」。
-- ===========================================================================

SET lock_timeout = '2s';
SET statement_timeout = '60s';

ALTER TABLE public.session_turns
    DROP CONSTRAINT IF EXISTS session_turns_origin_stage_check;

ALTER TABLE public.session_turns_hot
    DROP CONSTRAINT IF EXISTS session_turns_hot_origin_stage_check;
