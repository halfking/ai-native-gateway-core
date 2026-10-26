-- ===========================================================================
-- File:          sql/migrations/startup/875_ursm_snapshot_empty_response_rate_5m.down.sql
-- Migration:     875 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 875 —— 丢弃 empty_response_rate_5m 这一列。
--
-- **数据可完整取回**：与 818 的 down 同理 ——
--   875 把该键从 payload 提升为 typed 列的同时，把它加进了 writer.go 的
--   payloadDuplicateKeys，于是**从 875 上线那一刻起**新采集的行里，
--   该键不再出现在 payload 中。
--
--   ⇒ 若 down 在 875 上线**之前**执行：零数据损失（列本就不存在）。
--   ⇒ 若 down 在 875 上线**之后**执行：这期间的空响应率中间窗口
--     （5m 粒度）只能从两个兄弟列 _1m / _30m 推算，或从
--     5m 之前的旧二进制写入的 payload 里取。**5m 这一档本身不可还原。**
--
-- 所以顺序很重要：**先回退二进制，再执行本 down**，否则那档数据永久丢失。
-- 这一点比 818 的 down 更严格 —— 818 的 24 个键仍留在 payload 里
--（它们只被"重复剔除"，没有被"移动"），而 875 是真移动。
-- ===========================================================================
BEGIN;

ALTER TABLE public.ursm_node_snapshot_min
  DROP COLUMN IF EXISTS empty_response_rate_5m;

-- 恢复 818 写下的 payload 注释（875 把它改成了 41 项的口径）
COMMENT ON COLUMN public.ursm_node_snapshot_min.payload IS
  '818 起语义收窄：仅保留**未被 24 个 typed 列覆盖**的 hash 键（writer.go 已剔除 '
  'available/generation/source_priority/fail_streak/sr_1m/sr_5m/sr_30m 七个重复键）。'
  '保留 payload 的目的是保住 hash schema 演进时的前向兼容（见迁移头注释）。'
  '历史行的 payload 仍是全量 —— 本迁移刻意不回填（理由见迁移头注释）。';

COMMIT;
