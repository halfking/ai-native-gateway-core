-- ===========================================================================
-- File:          sql/migrations/startup/832_model_baseline_observation_health.down.sql
-- Migration:     832 (down)
-- Database:      llm_gateway
--
-- 只删本迁移建的表。索引与它同生共死（DROP TABLE 会带走）。
--
-- ⚠ 本 down 会丢弃**观察源的历史**（成功/失败时刻、连续失败次数、最后错误、
--   读到多少条观察价）。其中 `last_error` 与 `observed_models` 无法从别处重建：
--   前者只在抓取失败的那一刻存在，后者取决于当时那份 5MB 载荷的内容。
--   回滚后再跑一轮对账只能从零开始记，所以回滚前应先把这张表导出来。
--
-- 回滚它不会「恢复」静默：worker 侧不写这表时，抓取失败重新变成无痕。
--   ⇒ 本 down 单独执行等于自愿退回 828 之前那个状态，别把它当成无害清理。
--
-- 幂等：DROP ... IF EXISTS，可安全重放。
-- ===========================================================================
BEGIN;

DROP TABLE IF EXISTS public.model_baseline_price_observation_health;

COMMIT;
